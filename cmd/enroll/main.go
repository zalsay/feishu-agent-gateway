package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	core "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

const pairingTimeout = 15 * time.Minute
const confirmationTimeout = 20 * time.Second
const readyMarker = "FEISHU_ENROLL_READY"
const confirmationText = "配对已完成，网关正在启动，请稍后发送 /agents。"

type config struct {
	appID     string
	appSecret string
	token     string
	output    string
}

func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		appID:     getenv("FEISHU_APP_ID"),
		appSecret: getenv("FEISHU_APP_SECRET"),
		token:     getenv("FEISHU_ENROLL_TOKEN"),
		output:    getenv("FEISHU_ENROLL_OUTPUT"),
	}
	if cfg.appID == "" || cfg.appSecret == "" || !validToken(cfg.token) || cfg.output == "" {
		return config{}, errors.New("set FEISHU_APP_ID, FEISHU_APP_SECRET, FEISHU_ENROLL_OUTPUT, and a FEISHU_ENROLL_TOKEN of at least 16 ASCII letters or digits")
	}
	return cfg, nil
}

func validToken(token string) bool {
	if len(token) < 16 {
		return false
	}
	for _, char := range token {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

// extractOpenID returns an identity only for a single exact pairing command
// from a real user in a direct text conversation.
func extractOpenID(event *larkim.P2MessageReceiveV1, token string) string {
	if !validToken(token) || event == nil || event.Event == nil || event.Event.Message == nil || event.Event.Sender == nil {
		return ""
	}
	message := event.Event.Message
	sender := event.Event.Sender
	if message.ChatType == nil || *message.ChatType != "p2p" || message.MessageType == nil || *message.MessageType != "text" ||
		sender.SenderType == nil || *sender.SenderType != "user" || sender.SenderId == nil || sender.SenderId.OpenId == nil || *sender.SenderId.OpenId == "" ||
		message.Content == nil {
		return ""
	}

	decoder := json.NewDecoder(strings.NewReader(*message.Content))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return ""
	}
	key, err := decoder.Token()
	if err != nil || key != "text" {
		return ""
	}
	var text string
	if err := decoder.Decode(&text); err != nil {
		return ""
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return ""
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ""
	}
	if text != "/pair "+token {
		return ""
	}
	return *sender.SenderId.OpenId
}

func writeOpenID(path, openID string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	written, writeErr := io.WriteString(file, openID+"\n")
	if writeErr == nil && written != len(openID)+1 {
		writeErr = io.ErrShortWrite
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func writeReadyMarker(output io.Writer) error {
	_, err := fmt.Fprintln(output, readyMarker)
	return err
}

func confirmationContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, confirmationTimeout)
}

func sendConfirmation(ctx context.Context, client *lark.Client, openID string) error {
	content, err := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: confirmationText})
	if err != nil {
		return err
	}
	response, err := client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.CreateMessageV1ReceiveIDTypeOpenId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(openID).
			MsgType(larkim.MsgTypeText).
			Content(string(content)).
			Build()).
		Build())
	if err != nil {
		return err
	}
	if response == nil || !response.Success() {
		return errors.New("Feishu rejected the pairing confirmation")
	}
	return nil
}

func run() error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(signalCtx, pairingTimeout)
	defer cancel()

	api := lark.NewClient(cfg.appID, cfg.appSecret, lark.WithLogLevel(core.LogLevelError))
	events := dispatcher.NewEventDispatcher("", "")
	ws := larkws.NewClient(cfg.appID, cfg.appSecret,
		larkws.WithLogLevel(core.LogLevelError),
		larkws.WithEventHandler(events),
	)

	ws.SetOnReady(func() {
		_ = writeReadyMarker(os.Stdout)
	})

	result := make(chan error, 1)
	var matchMu sync.Mutex
	matched := false
	events.OnP2MessageReceiveV1(func(eventCtx context.Context, event *larkim.P2MessageReceiveV1) error {
		openID := extractOpenID(event, cfg.token)
		if openID == "" {
			return nil
		}

		matchMu.Lock()
		defer matchMu.Unlock()
		if matched {
			return nil
		}
		matched = true

		if err := writeOpenID(cfg.output, openID); err != nil {
			result <- fmt.Errorf("create enrollment output: %w", err)
			cancel()
			return nil
		}
		ackCtx, ackCancel := confirmationContext(eventCtx)
		defer ackCancel()
		if err := sendConfirmation(ackCtx, api, openID); err != nil {
			result <- fmt.Errorf("send pairing confirmation: %w", err)
			cancel()
			return nil
		}
		result <- nil
		cancel()
		return nil
	})

	startResult := make(chan error, 1)
	go func() {
		startResult <- ws.Start(ctx)
	}()

	startErr := <-startResult // Start waits for its WebSocket workers before returning.
	select {
	case resultErr := <-result:
		return resultErr
	default:
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.New("pairing timed out after 15 minutes")
	}
	if signalCtx.Err() != nil {
		return nil
	}
	if startErr != nil {
		return fmt.Errorf("Feishu WebSocket stopped: %w", startErr)
	}
	return errors.New("Feishu WebSocket stopped before pairing")
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
