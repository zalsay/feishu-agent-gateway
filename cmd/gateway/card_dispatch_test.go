package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"feishu-agent-gateway/internal/gateway"
	"github.com/larksuite/oapi-sdk-go/v3/channel/normalize"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestCardCallbackDispatchesThroughOfficialEventSchema(t *testing.T) {
	g := menuGateway(t)
	if _, err := g.Handle(context.Background(), gateway.Message{EventID: "select", ChatID: "chat", UserID: "u", ChatType: "p2p", Content: "/use codex"}); err != nil {
		t.Fatal(err)
	}
	_, nonce, err := g.PrepareSettingCard("chat", "u", "reasoning")
	if err != nil {
		t.Fatal(err)
	}
	d := dispatcher.NewEventDispatcher("", "")
	d.OnP2CardActionTrigger(func(ctx context.Context, e *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
		return handleCardAction(ctx, g, map[string]bool{"u": true}, normalize.ParseCardAction(e))
	})
	req := &larkevent.EventReq{Header: http.Header{}, Body: []byte(fmt.Sprintf(`{"schema":"2.0","header":{"event_id":"card-1","event_type":"card.action.trigger","create_time":"1700000000000"},"event":{"operator":{"open_id":"u"},"context":{"open_message_id":"msg","open_chat_id":"chat"},"action":{"tag":"button","value":{"agent":"codex","kind":"reasoning","value":"medium","expected":"high","nonce":"%s"}}}}`, nonce))}
	response := d.Handle(context.Background(), req)
	if response == nil || response.Body == nil || !strings.Contains(string(response.Body), "medium") {
		t.Fatalf("official SDK callback response: %+v", response)
	}
	state, err := g.SelectedSettings("chat", "u")
	if err != nil || state.Reasoning != "medium" {
		t.Fatalf("official callback not applied: %+v %v", state, err)
	}
}
