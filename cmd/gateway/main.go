package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"feishu-agent-gateway/internal/feishu"
	"feishu-agent-gateway/internal/gateway"
	"feishu-agent-gateway/internal/registry"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/channel"
	"github.com/larksuite/oapi-sdk-go/v3/channel/normalize"
	"github.com/larksuite/oapi-sdk-go/v3/channel/types"
	core "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkapp "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

type config struct {
	appID, appSecret, statePath, agentConfigPath string
	allowed                                      []string
}

func loadConfig(get func(string) string) (config, error) {
	c := config{appID: get("FEISHU_APP_ID"), appSecret: get("FEISHU_APP_SECRET"), statePath: get("GATEWAY_STATE_PATH"), agentConfigPath: get("AGENT_CONFIG_PATH")}
	for _, raw := range strings.Split(get("FEISHU_ALLOWED_USER_IDS"), ",") {
		if id := strings.TrimSpace(raw); id != "" {
			c.allowed = append(c.allowed, id)
		}
	}
	if c.statePath == "" {
		c.statePath = "state.json"
	}
	if c.appID == "" || c.appSecret == "" || len(c.allowed) == 0 {
		return c, errors.New("set FEISHU_APP_ID, FEISHU_APP_SECRET and FEISHU_ALLOWED_USER_IDS")
	}
	return c, nil
}

func filterMessageError(m gateway.Message, allowed map[string]bool) error {
	if m.ChatType != "p2p" {
		return gateway.ErrIgnored
	}
	if !allowed[m.UserID] {
		return gateway.ErrUnauthorized
	}
	return nil
}
func filterMessage(m gateway.Message, rawType string, allowed map[string]bool) string {
	if filterMessageError(m, allowed) != nil {
		return ""
	}
	if rawType != "text" {
		return "仅支持文本消息，请改用文字描述问题。"
	}
	return ""
}
func chunkText(s string, maxRunes int) []string {
	r := []rune(s)
	if maxRunes < 1 {
		return nil
	}
	chunks := make([]string, 0, (len(r)+maxRunes-1)/maxRunes)
	for len(r) > 0 {
		n := min(len(r), maxRunes)
		chunks = append(chunks, string(r[:n]))
		r = r[n:]
	}
	return chunks
}
func sendReply(ctx context.Context, ch types.Channel, msg *types.NormalizedMessage, text string) error {
	for i, part := range chunkText(text, 3000) {
		input := &types.SendInput{ChatID: msg.ChatID, Text: part}
		if i == 0 {
			input.ReplyMessageID = msg.MessageID
		}
		if _, err := ch.Send(ctx, input); err != nil {
			return err
		}
	}
	return nil
}

func newWSClient(appID, appSecret string) *larkws.Client {
	return larkws.NewClient(appID, appSecret, larkws.WithLogLevel(core.LogLevelWarn), larkws.WithEventHandler(dispatcher.NewEventDispatcher("", "")))
}

func run() error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	backends, err := registry.Load(cfg.agentConfigPath, os.Getenv)
	if err != nil {
		return err
	}
	g, err := gateway.New(cfg.statePath, cfg.allowed, backends)
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for name := range backends {
		names[name] = true
	}

	allowed := map[string]bool{}
	for _, id := range cfg.allowed {
		allowed[id] = true
	}
	api := lark.NewClient(cfg.appID, cfg.appSecret, lark.WithLogLevel(core.LogLevelWarn))
	ws := newWSClient(cfg.appID, cfg.appSecret)
	ch := channel.NewChannel(api, nil) // outbound only; inbound uses direct WS dispatch without hidden batching
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	work := make(chan struct{}, 8)
	var wg sync.WaitGroup
	ws.EventHandler().OnP2MessageReceiveV1(func(_ context.Context, event *larkim.P2MessageReceiveV1) error {
		msg := normalize.ParseMessage(event)
		if msg == nil {
			return nil
		}
		m := gateway.Message{EventID: msg.EventID, ChatID: msg.ChatID, ChatType: msg.ChatType, UserID: msg.UserID, Content: msg.Content}
		if filterMessageError(m, allowed) != nil {
			return nil
		}
		select {
		case work <- struct{}{}:
		default:
			busyCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return sendReply(busyCtx, ch, msg, "当前任务较多，请稍后重试。")
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-work }()
			turnCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			if reply := filterMessage(m, msg.RawContentType, allowed); reply != "" {
				if err := sendReply(turnCtx, ch, msg, reply); err != nil {
					log.Printf("reply event %s: %v", m.EventID, err)
				}
				return
			}
			text, err := g.Handle(turnCtx, m)
			if err != nil {
				if errors.Is(err, gateway.ErrBusy) {
					text = "当前会话仍在处理上一条消息，请稍后重试。"
				} else {
					log.Printf("handle event %s: %v", m.EventID, err)
					text = "处理失败，请稍后重试；如果任务可能已执行，请先检查后端状态。"
				}
			}
			if text != "" {
				replyCtx, replyCancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer replyCancel()
				if err := sendReply(replyCtx, ch, msg, text); err != nil {
					log.Printf("reply event %s: %v", m.EventID, err)
				}
			}
		}()
		return nil
	})
	ws.EventHandler().OnP2BotMenuV6(func(_ context.Context, event *larkapp.P2BotMenuV6) error {
		action, err := feishu.ParseMenu(event, names)
		if err != nil || !allowed[action.UserID] {
			return nil
		}
		select {
		case work <- struct{}{}:
		default:
			busyCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := ch.Send(busyCtx, &types.SendInput{UserID: action.UserID, Text: "当前任务较多，请稍后再操作菜单。"})
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-work }()
			menuCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := handleMenu(menuCtx, g, allowed, names, ch, event); err != nil {
				log.Printf("menu event %s: %v", action.EventID, err)
			}
		}()
		return nil
	})

	ws.EventHandler().OnP2CardActionTrigger(func(eventCtx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
		result, err := handleCardAction(eventCtx, g, allowed, normalize.ParseCardAction(event))
		if err != nil {
			log.Printf("card callback error: %v", err)
		}
		return result, err
	})
	ws.SetOnReady(func() { log.Print("Feishu WebSocket ready") })
	ws.SetOnError(func(err error) { log.Printf("Feishu WebSocket error: %v", err) })
	result := make(chan error, 1)
	go func() { result <- ws.Start(ctx) }()
	select {
	case err = <-result:
		if ctx.Err() == nil {
			stop()
			wg.Wait()
			return fmt.Errorf("Feishu channel stopped: %w", err)
		}
	case <-ctx.Done():
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if stopErr := ws.CloseAndWait(stopCtx); stopErr != nil {
			log.Printf("stop Feishu WebSocket: %v", stopErr)
		}
		cancel()
		err = <-result
	}
	wg.Wait()
	if err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
