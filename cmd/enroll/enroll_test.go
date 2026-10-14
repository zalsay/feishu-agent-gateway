package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestExtractOpenID(t *testing.T) {
	valid := func() *larkim.P2MessageReceiveV1 {
		chatType := "p2p"
		messageType := "text"
		content := `{"text":"/pair 0123456789abcdef"}`
		senderType := "user"
		openID := "ou_test-user"
		return &larkim.P2MessageReceiveV1{
			Event: &larkim.P2MessageReceiveV1Data{
				Message: &larkim.EventMessage{
					ChatType:    &chatType,
					MessageType: &messageType,
					Content:     &content,
				},
				Sender: &larkim.EventSender{
					SenderType: &senderType,
					SenderId:   &larkim.UserId{OpenId: &openID},
				},
			},
		}
	}

	tests := []struct {
		name   string
		mutate func(*larkim.P2MessageReceiveV1)
		want   string
	}{
		{name: "matching user p2p text extracts open id", want: "ou_test-user"},
		{
			name: "group is rejected",
			mutate: func(event *larkim.P2MessageReceiveV1) {
				value := "group"
				event.Event.Message.ChatType = &value
			},
		},
		{
			name: "non-text is rejected",
			mutate: func(event *larkim.P2MessageReceiveV1) {
				value := "post"
				event.Event.Message.MessageType = &value
			},
		},
		{
			name: "wrong token is rejected",
			mutate: func(event *larkim.P2MessageReceiveV1) {
				value := `{"text":"/pair wrong-token-000000"}`
				event.Event.Message.Content = &value
			},
		},
		{
			name: "bot sender is rejected",
			mutate: func(event *larkim.P2MessageReceiveV1) {
				value := "bot"
				event.Event.Sender.SenderType = &value
			},
		},
		{
			name: "missing sender identity is rejected",
			mutate: func(event *larkim.P2MessageReceiveV1) {
				event.Event.Sender.SenderId = nil
			},
		},
		{
			name: "empty open id is rejected",
			mutate: func(event *larkim.P2MessageReceiveV1) {
				value := ""
				event.Event.Sender.SenderId.OpenId = &value
			},
		},
		{
			name: "extra content property is rejected",
			mutate: func(event *larkim.P2MessageReceiveV1) {
				value := `{"text":"/pair 0123456789abcdef","extra":"value"}`
				event.Event.Message.Content = &value
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := valid()
			if test.mutate != nil {
				test.mutate(event)
			}
			if got := extractOpenID(event, "0123456789abcdef"); got != test.want {
				t.Fatalf("extractOpenID() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestValidateToken(t *testing.T) {
	tests := []struct {
		name  string
		token string
		valid bool
	}{
		{name: "16 ASCII alphanumeric characters", token: "aB0123456789wxyz", valid: true},
		{name: "too short", token: "aB0123456789wxy", valid: false},
		{name: "punctuation", token: "aB0123456789wxy!", valid: false},
		{name: "non-ASCII", token: "中文0123456789ab", valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validToken(test.token); got != test.valid {
				t.Fatalf("validToken(%q) = %v, want %v", test.token, got, test.valid)
			}
		})
	}
}

func TestWriteOpenIDDoesNotOverwriteExistingOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paired-open-id")
	const original = "existing enrollment data"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	if err := writeOpenID(path, "ou_new-user"); err == nil {
		t.Fatal("writeOpenID() unexpectedly overwrote an existing file")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("existing output changed to %q", got)
	}
}

func TestWriteReadyMarker(t *testing.T) {
	var output bytes.Buffer
	if err := writeReadyMarker(&output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "FEISHU_ENROLL_READY\n"; got != want {
		t.Fatalf("writeReadyMarker() = %q, want %q", got, want)
	}
}

func TestConfirmationContextHas20SecondDeadlineAndInheritsCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()

	started := time.Now()
	ctx, cancel := confirmationContext(parent)
	created := time.Now()
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("confirmation context has no deadline")
	}
	if deadline.Before(started.Add(20*time.Second)) || deadline.After(created.Add(20*time.Second)) {
		t.Fatalf("confirmation deadline %v is not 20 seconds after context creation", deadline)
	}

	cancelParent()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("confirmation context did not inherit parent cancellation")
	}
}
