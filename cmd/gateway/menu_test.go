package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"feishu-agent-gateway/internal/gateway"
	"github.com/larksuite/oapi-sdk-go/v3/channel/types"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	larkapp "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
)

type menuAgent struct{ name string }

func (a menuAgent) Models() []string  { return []string{"gpt-6-luna", "gpt-6-sol"} }
func (a menuAgent) Efforts() []string { return []string{"low", "medium", "high", "xhigh"} }
func (a menuAgent) RunWithOptions(ctx context.Context, prompt, continuation, scope string, opts gateway.RunOptions) (string, string, error) {
	return a.Run(ctx, prompt, continuation, scope)
}
func (a menuAgent) Run(context.Context, string, string, string) (string, string, error) {
	return a.name, "session", nil
}

type fakeMenuSender struct {
	sent []*types.SendInput
	chat string
	err  error
}

func (s *fakeMenuSender) Send(_ context.Context, input *types.SendInput) (*types.SendResult, error) {
	copyInput := *input
	s.sent = append(s.sent, &copyInput)
	if s.err != nil {
		return nil, s.err
	}
	return &types.SendResult{ChatID: s.chat, MessageID: "m"}, nil
}
func eventForMenu(user, key, id string) *larkapp.P2BotMenuV6 {
	return &larkapp.P2BotMenuV6{EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{EventID: id}}, Event: &larkapp.P2BotMenuV6Data{EventKey: &key, Operator: &larkapp.Operator{OperatorId: &larkapp.UserId{OpenId: &user}}}}
}
func menuGateway(t *testing.T) *gateway.Gateway {
	t.Helper()
	g, err := gateway.New(filepath.Join(t.TempDir(), "state.json"), []string{"u"}, map[string]gateway.Agent{"codex": menuAgent{"codex"}, "hermes": menuAgent{"hermes"}})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestMenuUsesExistingPrivateConversationAndDedup(t *testing.T) {
	g := menuGateway(t)
	ctx := context.Background()
	_, err := g.Handle(ctx, gateway.Message{EventID: "message1", ChatID: "c1", UserID: "u", ChatType: "p2p", Content: "/use hermes"})
	if err != nil {
		t.Fatal(err)
	}
	sender := &fakeMenuSender{chat: "c1"}
	allowed := map[string]bool{"u": true}
	names := map[string]bool{"codex": true, "hermes": true}
	event := eventForMenu("u", "agent_codex", "menu1")
	if err := handleMenu(ctx, g, allowed, names, sender, event); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 1 || sender.sent[0].ChatID != "c1" || sender.sent[0].UserID != "" || sender.sent[0].Text != "已切换至 codex。" {
		t.Fatalf("sent=%+v", sender.sent)
	}
	if err := handleMenu(ctx, g, allowed, names, sender, event); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 1 {
		t.Fatal("duplicate menu produced another reply")
	}
	answer, err := g.Handle(ctx, gateway.Message{EventID: "message2", ChatID: "c1", UserID: "u", ChatType: "p2p", Content: "question"})
	if err != nil || answer != "【codex】codex" {
		t.Fatalf("selected state not reused: %q %v", answer, err)
	}
}

func TestMenuBootstrapsChatOnlyForAuthorizedActor(t *testing.T) {
	g := menuGateway(t)
	ctx := context.Background()
	allowed := map[string]bool{"u": true}
	names := map[string]bool{"codex": true}
	sender := &fakeMenuSender{chat: "real-private-chat"}
	if err := handleMenu(ctx, g, allowed, names, sender, eventForMenu("outsider", "agent_codex", "1")); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 0 {
		t.Fatal("unauthorized actor caused a Feishu send")
	}
	if err := handleMenu(ctx, g, allowed, names, sender, eventForMenu("u", "unrecognized", "2")); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 0 {
		t.Fatal("unknown menu caused a Feishu send")
	}
	if err := handleMenu(ctx, g, allowed, names, sender, eventForMenu("u", "agent_codex", "3")); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 2 || sender.sent[0].UserID != "u" || sender.sent[1].ChatID != "real-private-chat" {
		t.Fatalf("wrong bootstrap routing: %+v", sender.sent)
	}
	if chat, ok := g.ChatIDForUser("u"); !ok || chat != "real-private-chat" {
		t.Fatalf("saved synthetic chat: %q", chat)
	}
}

func TestMenuRequiresFeishuChatResult(t *testing.T) {
	g := menuGateway(t)
	sender := &fakeMenuSender{}
	err := handleMenu(context.Background(), g, map[string]bool{"u": true}, nil, sender, eventForMenu("u", "gateway_help", "1"))
	if err == nil || !strings.Contains(err.Error(), "chat ID") {
		t.Fatalf("missing chat result: %v", err)
	}
	if _, ok := g.ChatIDForUser("u"); ok {
		t.Fatal("mutated state without a real chat ID")
	}
	sender = &fakeMenuSender{err: errors.New("transport")}
	if err := handleMenu(context.Background(), g, map[string]bool{"u": true}, nil, sender, eventForMenu("u", "gateway_help", "2")); err == nil {
		t.Fatal("ignored failed bootstrap")
	}
}

func TestSettingsMenuSendsWhitelistedCard(t *testing.T) {
	g := menuGateway(t)
	ctx := context.Background()
	_, err := g.Handle(ctx, gateway.Message{EventID: "select", ChatID: "chat", UserID: "u", ChatType: "p2p", Content: "/use codex"})
	if err != nil {
		t.Fatal(err)
	}
	sender := &fakeMenuSender{chat: "chat"}
	if err := handleMenu(ctx, g, map[string]bool{"u": true}, map[string]bool{"codex": true}, sender, eventForMenu("u", "settings_model", "menu1")); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 1 || sender.sent[0].Card == "" || sender.sent[0].ChatID != "chat" {
		t.Fatalf("no model card: %+v", sender.sent)
	}
	var card struct {
		Schema string `json:"schema"`
		Body   struct {
			Elements []struct {
				Tag       string `json:"tag"`
				Behaviors []struct {
					Type  string            `json:"type"`
					Value map[string]string `json:"value"`
				} `json:"behaviors"`
			} `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(sender.sent[0].Card), &card); err != nil {
		t.Fatal(err)
	}
	if card.Schema != "2.0" || len(card.Body.Elements) != 3 {
		t.Fatalf("invalid card %+v", card)
	}
	values := map[string]bool{}
	for _, elem := range card.Body.Elements {
		if elem.Tag == "button" && len(elem.Behaviors) == 1 && elem.Behaviors[0].Type == "callback" {
			values[elem.Behaviors[0].Value["value"]] = true
		}
	}
	if len(values) != 2 {
		t.Fatalf("want exactly two selectable models, got %v", values)
	}
	for _, value := range []string{"gpt-6-luna", "gpt-6-sol"} {
		if !values[value] {
			t.Fatalf("missing %s in %v", value, values)
		}
	}
}

func TestCardActionAuthenticatesCurrentSelection(t *testing.T) {
	g := menuGateway(t)
	ctx := context.Background()
	_, err := g.Handle(ctx, gateway.Message{EventID: "select", ChatID: "chat", UserID: "u", ChatType: "p2p", Content: "/use codex"})
	if err != nil {
		t.Fatal(err)
	}
	makeEvent := func(id, user, chat, agent, kind, value string) *types.CardActionEvent {
		return &types.CardActionEvent{EventID: id, ChatID: chat, Operator: types.CardActionOperator{OpenID: user}, Action: types.CardActionPayload{Tag: "button", Value: map[string]any{"agent": agent, "kind": kind, "value": value, "expected": "", "nonce": "test-card"}}}
	}
	allowed := map[string]bool{"u": true}
	for _, ev := range []*types.CardActionEvent{makeEvent("1", "outsider", "chat", "codex", "model", "gpt-6-sol"), makeEvent("2", "u", "other", "codex", "model", "gpt-6-sol"), makeEvent("3", "u", "chat", "hermes", "model", "gpt-6-sol"), makeEvent("4", "u", "chat", "codex", "model", "--unknown")} {
		_, err := handleCardAction(ctx, g, allowed, ev)
		if err != nil {
			t.Fatal(err)
		}
		state, err := g.SelectedSettings("chat", "u")
		if err != nil || state.Model != "" {
			t.Fatalf("untrusted card changed setting: %+v %v", state, err)
		}
	}
	_, issuedNonce, err := g.PrepareSettingCard("chat", "u", "reasoning")
	if err != nil {
		t.Fatal(err)
	}
	ev := makeEvent("5", "u", "chat", "codex", "reasoning", "medium")
	ev.Action.Value["expected"] = "high"
	ev.Action.Value["nonce"] = issuedNonce
	result, err := handleCardAction(ctx, g, allowed, ev)
	if err != nil || result == nil || result.Toast == nil || !strings.Contains(result.Toast.Content, "medium") {
		t.Fatalf("valid card response %+v %v", result, err)
	}
	_, err = handleCardAction(ctx, g, allowed, ev)
	if err != nil {
		t.Fatal(err)
	}
	state, err := g.SelectedSettings("chat", "u")
	if err != nil || state.Reasoning != "medium" {
		t.Fatalf("card not persisted %+v %v", state, err)
	}
	if _, err = g.Handle(ctx, gateway.Message{EventID: "select2", ChatID: "chat", UserID: "u", ChatType: "p2p", Content: "/use hermes"}); err != nil {
		t.Fatal(err)
	}
	_, err = handleCardAction(ctx, g, allowed, makeEvent("6", "u", "chat", "codex", "model", "gpt-6-sol"))
	if err != nil {
		t.Fatal(err)
	}
	state, _ = g.SelectedSettings("chat", "u")
	if state.Agent != "hermes" {
		t.Fatalf("stale card applied %+v", state)
	}
}

func TestCardCallbackRequiresCurrentUnchangedSelectionAndFreshCard(t *testing.T) {
	ctx := context.Background()
	g := menuGateway(t)
	_, err := g.Handle(ctx, gateway.Message{EventID: "select", ChatID: "chat", UserID: "u", ChatType: "p2p", Content: "/use codex"})
	if err != nil {
		t.Fatal(err)
	}
	sender := &fakeMenuSender{chat: "chat"}
	allowed := map[string]bool{"u": true}
	names := map[string]bool{"codex": true}
	if err := handleMenu(ctx, g, allowed, names, sender, eventForMenu("u", "settings_model", "menu-1")); err != nil {
		t.Fatal(err)
	}
	var card struct {
		Body struct {
			Elements []struct {
				Behaviors []struct {
					Value map[string]string `json:"value"`
				} `json:"behaviors"`
			} `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(sender.sent[0].Card), &card); err != nil {
		t.Fatal(err)
	}
	var value map[string]string
	for _, element := range card.Body.Elements {
		if len(element.Behaviors) > 0 && element.Behaviors[0].Value["value"] == "gpt-6-sol" {
			value = element.Behaviors[0].Value
		}
	}
	if value == nil {
		t.Fatal("missing model choice")
	}
	selectedModel := func(id string) error {
		event := &types.CardActionEvent{EventID: id, ChatID: "chat", Operator: types.CardActionOperator{OpenID: "u"}, Action: types.CardActionPayload{Tag: "button", Value: map[string]any{"agent": value["agent"], "kind": value["kind"], "value": value["value"], "expected": value["expected"], "nonce": value["nonce"]}}}
		_, err := handleCardAction(ctx, g, allowed, event)
		return err
	}
	if err := selectedModel("card-1"); err != nil {
		t.Fatal(err)
	}
	selected, _ := g.SelectedSettings("chat", "u")
	if selected.Model != "gpt-6-sol" {
		t.Fatalf("first choice not applied %+v", selected)
	}
	if _, err := g.Handle(ctx, gateway.Message{EventID: "manual", ChatID: "chat", UserID: "u", ChatType: "p2p", Content: "/model gpt-6-luna"}); err != nil {
		t.Fatal(err)
	}
	if err := selectedModel("card-2"); err != nil {
		t.Fatal(err)
	}
	selected, _ = g.SelectedSettings("chat", "u")
	if selected.Model != "gpt-6-luna" {
		t.Fatalf("stale card overrode newer choice %+v", selected)
	}
}

func TestCardModelSwitchThenSelectedReasoningPreservesSelection(t *testing.T) {
	g := menuGateway(t)
	ctx := context.Background()
	allowed := map[string]bool{"u": true}
	_, err := g.Handle(ctx, gateway.Message{EventID: "select", ChatID: "chat", UserID: "u", ChatType: "p2p", Content: "/use codex"})
	if err != nil {
		t.Fatal(err)
	}
	event := func(id, kind, value, expected string) *types.CardActionEvent {
		return &types.CardActionEvent{EventID: id, ChatID: "chat", Operator: types.CardActionOperator{OpenID: "u"}, Action: types.CardActionPayload{Tag: "button", Value: map[string]any{"agent": "codex", "kind": kind, "value": value, "expected": expected, "nonce": "menu-before"}}}
	}
	for _, ev := range []*types.CardActionEvent{event("first", "model", "gpt-6-sol", ""), event("second", "reasoning", "xhigh", "high")} {
		_, nonce, err := g.PrepareSettingCard("chat", "u", ev.Action.Value["kind"].(string))
		if err != nil {
			t.Fatal(err)
		}
		ev.Action.Value["nonce"] = nonce
		resp, err := handleCardAction(ctx, g, allowed, ev)
		if err != nil || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
			t.Fatalf("action rejected: %+v %v", resp, err)
		}
	}
	state, err := g.SelectedSettings("chat", "u")
	if err != nil || state.Model != "gpt-6-sol" || state.Reasoning != "xhigh" {
		t.Fatalf("selected settings %+v %v", state, err)
	}
}
