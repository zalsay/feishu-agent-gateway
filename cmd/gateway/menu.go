package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"feishu-agent-gateway/internal/feishu"
	"feishu-agent-gateway/internal/gateway"
	"github.com/larksuite/oapi-sdk-go/v3/channel/types"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkapp "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
)

type menuSender interface {
	Send(context.Context, *types.SendInput) (*types.SendResult, error)
}

func handleMenu(ctx context.Context, g *gateway.Gateway, allowed, names map[string]bool, sender menuSender, event *larkapp.P2BotMenuV6) error {
	action, err := feishu.ParseMenu(event, names)
	if errors.Is(err, feishu.ErrInvalidMenu) {
		return nil
	}
	if err != nil {
		return err
	}
	// Visibility of a native menu is not permission to use a backend Agent.
	if !allowed[action.UserID] {
		return nil
	}
	chat, known := g.ChatIDForUser(action.UserID)
	if !known {
		result, err := sender.Send(ctx, &types.SendInput{UserID: action.UserID, Text: "已收到菜单操作，正在处理。"})
		if err != nil {
			return fmt.Errorf("resolve menu private chat: %w", err)
		}
		if result == nil || result.ChatID == "" {
			return errors.New("Feishu did not return a private chat ID for the menu actor")
		}
		chat = result.ChatID
	}
	if action.Command == "/settings model" || action.Command == "/settings reasoning" {
		settings, nonce, err := g.PrepareSettingCard(chat, action.UserID, map[string]string{"/settings model": "model", "/settings reasoning": "reasoning"}[action.Command])
		if err != nil {
			_, sendErr := sender.Send(ctx, &types.SendInput{ChatID: chat, Text: "请先通过菜单选择已部署的 Agent。"})
			return sendErr
		}
		kind := "model"
		if action.Command == "/settings reasoning" {
			kind = "reasoning"
		}
		card, err := settingCard(settings, kind, nonce)
		if err != nil {
			return err
		}
		_, err = sender.Send(ctx, &types.SendInput{ChatID: chat, Card: card})
		return err
	}
	reply, err := g.Handle(ctx, gateway.Message{EventID: "menu:" + action.EventID, ChatID: chat, UserID: action.UserID, ChatType: "p2p", Content: action.Command})
	if errors.Is(err, gateway.ErrBusy) {
		reply = "当前会话仍在处理中，请稍后再操作菜单。"
	} else if err != nil {
		return fmt.Errorf("menu operation: %w", err)
	}
	for _, part := range chunkText(reply, 3000) {
		if _, err := sender.Send(ctx, &types.SendInput{ChatID: chat, Text: part}); err != nil {
			return err
		}
	}
	return nil
}

func settingCard(settings gateway.SelectedSettings, kind, eventID string) (string, error) {
	options := settings.Models
	current := settings.Model
	title := "选择模型"
	if kind == "reasoning" {
		options = settings.Efforts
		current = settings.Reasoning
		title = "思考强度"
	}
	if kind != "reasoning" && kind != "model" {
		return "", errors.New("invalid settings card kind")
	}
	if len(options) == 0 {
		return "", errors.New("selected Agent has no settings options")
	}
	elements := []any{map[string]any{"tag": "markdown", "content": "当前 Agent：" + settings.Agent + "　当前" + title + "：" + displayCurrent(current)}}
	for _, option := range options {
		caption := option
		if current == option {
			caption += " ✓"
		}
		elements = append(elements, map[string]any{"tag": "button", "text": map[string]string{"tag": "plain_text", "content": caption}, "type": "default", "behaviors": []any{map[string]any{"type": "callback", "value": map[string]string{"agent": settings.Agent, "kind": kind, "value": option, "expected": current, "nonce": eventID}}}})
	}
	card := map[string]any{"schema": "2.0", "header": map[string]any{"title": map[string]string{"tag": "plain_text", "content": title}}, "body": map[string]any{"elements": elements}}
	bytes, err := json.Marshal(card)
	return string(bytes), err
}
func displayCurrent(value string) string {
	if value == "" {
		return "服务端默认"
	}
	return value
}

func handleCardAction(ctx context.Context, g *gateway.Gateway, allowed map[string]bool, event *types.CardActionEvent) (*callback.CardActionTriggerResponse, error) {
	invalid := &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "warning", Content: "操作已失效，请重新打开参数设置菜单。"}}
	if event == nil || event.EventID == "" || event.ChatID == "" || event.Operator.OpenID == "" || !allowed[event.Operator.OpenID] || event.Action.Tag != "button" {
		return invalid, nil
	}
	chat, ok := g.ChatIDForUser(event.Operator.OpenID)
	if !ok || chat != event.ChatID {
		return invalid, nil
	}
	agentName, _ := event.Action.Value["agent"].(string)
	kind, _ := event.Action.Value["kind"].(string)
	value, _ := event.Action.Value["value"].(string)
	expected, hasExpected := event.Action.Value["expected"].(string)
	nonce, hasNonce := event.Action.Value["nonce"].(string)
	if !hasExpected || !hasNonce || kind != "model" && kind != "reasoning" {
		return invalid, nil
	}
	reply, err := g.ApplySettingCard(chat, event.Operator.OpenID, event.EventID, nonce, agentName, kind, value, expected)
	if err != nil {
		if errors.Is(err, gateway.ErrUnauthorized) {
			return invalid, nil
		}
		return invalid, nil
	}

	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "success", Content: reply}}, nil
}
func containsMenuOption(options []string, value string) bool {
	for _, s := range options {
		if s == value {
			return true
		}
	}
	return false
}
