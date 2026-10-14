package agents

import (
	"context"
	"errors"
	"feishu-agent-gateway/internal/gateway"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHermesContinuationAndFailures(t *testing.T) {
	var previous []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/responses/resp-2" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-Hermes-Session-Key") != "scope-1" {
			t.Errorf("request %s %s", r.URL.Path, r.Header.Get("Authorization"))
			w.WriteHeader(401)
			return
		}
		var req struct {
			Input, PreviousResponseID string
			Store                     bool
		}
		// exact JSON keys verified by raw body in the adapter test below
		_ = req
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		previous = append(previous, string(body))
		if strings.Contains(string(body), "fail") {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":"secret-internal"}`)
			return
		}
		fmt.Fprint(w, `{"id":"resp-2","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"},{"type":"output_text","text":" world"}]}]}`)
	}))
	defer server.Close()
	a, err := NewHermes(server.URL, "secret", "hermes-agent", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	text, next, err := a.Run(context.Background(), "first", "", "scope-1")
	if err != nil || text != "hello world" || next != "resp-2" {
		t.Fatalf("first %q %q %v", text, next, err)
	}
	_, _, err = a.Run(context.Background(), "second", "resp-2", "scope-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(previous[1], `"previous_response_id":"resp-2"`) || !strings.Contains(previous[1], `"store":true`) {
		t.Fatalf("continuation %s", previous[1])
	}
	if _, _, err = a.Run(context.Background(), "fail", "resp-2", "scope-1"); err == nil || strings.Contains(err.Error(), "secret-internal") {
		t.Fatalf("unsafe error %v", err)
	}
}

func TestCodexJSONLAndResume(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-codex")
	log := filepath.Join(dir, "args")
	source := `#!/bin/sh
if [ "$1" = "app-server" ]; then
 read init; printf '%s\n' '{"id":1,"result":{}}'
 read ready; read check; printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-1"}}}'
 exit 0
fi
printf '%s\n' "$@" > "` + log + `"
printf '%s\n' '{"type":"thread.started","thread_id":"thread-1"}' '{"type":"item.completed","item":{"type":"agent_message","text":"result"}}' '{"type":"turn.completed"}'
`
	if err := os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	a, err := NewCodex(script, dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	text, next, err := a.Run(context.Background(), "fix it", "", "scope-1")
	if err != nil || text != "result" || next != "thread-1" {
		t.Fatalf("new %q %q %v", text, next, err)
	}
	args, _ := os.ReadFile(log)
	if !strings.Contains(string(args), "read-only") || !strings.Contains(string(args), "--json") || !strings.Contains(string(args), "--\nfix it") {
		t.Fatalf("unsafe flags %s", args)
	}
	_, _, err = a.Run(context.Background(), "continue", "thread-1", "scope-1")
	if err != nil {
		t.Fatal(err)
	}
	args, _ = os.ReadFile(log)
	if !strings.Contains(string(args), "resume\n") || !strings.Contains(string(args), "thread-1\n") || !strings.Contains(string(args), "sandbox_mode=\"read-only\"") {
		t.Fatalf("resume flags %s", args)
	}
}

func TestCodexFailedTurnDoesNotReturnAnswer(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	source := `#!/bin/sh
printf '%s\n' '{"type":"thread.started","thread_id":"x"}' '{"type":"item.completed","item":{"type":"agent_message","text":"partial"}}' '{"type":"turn.failed"}'
`
	if err := os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	a, _ := NewCodex(script, dir, dir)
	if _, _, err := a.Run(context.Background(), "x", "", "scope-1"); err == nil {
		t.Fatal("failed turn must not commit")
	}
}

func TestHermesRejectsRemotePlainHTTP(t *testing.T) {
	for _, raw := range []string{"http://hermes.internal:8642", "http://192.168.1.1:8642"} {
		if _, err := NewHermes(raw, "secret", "hermes-agent", nil); err == nil {
			t.Fatalf("allowed plaintext remote: %s", raw)
		}
	}
	for _, raw := range []string{"http://127.0.0.1:8642", "http://localhost:8642", "http://[::1]:8642", "https://hermes.internal"} {
		if _, err := NewHermes(raw, "secret", "hermes-agent", nil); err != nil {
			t.Fatalf("rejected %s: %v", raw, err)
		}
	}
}

func TestHermesInvalidPreviousResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("sent prompt before checking continuation")
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	a, _ := NewHermes(server.URL, "key", "model", server.Client())
	_, _, err := a.Run(context.Background(), "next", "expired", "scope")
	if !errors.Is(err, gateway.ErrInvalidContinuation) {
		t.Fatalf("expected invalid continuation: %v", err)
	}
}

func TestCodexMissingSessionFailsBeforeExec(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake")
	flag := filepath.Join(dir, "ran")
	code := `#!/bin/sh
if [ "$1" = "app-server" ]; then
 read init; printf '%s\n' '{"id":1,"result":{}}'
 read ready; read check; printf '%s\n' '{"id":2,"error":{"code":-32600,"message":"thread not loaded"}}'
 exit 0
fi
touch "` + flag + `"
`
	if err := os.WriteFile(script, []byte(code), 0700); err != nil {
		t.Fatal(err)
	}
	a, err := NewCodex(script, dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = a.Run(context.Background(), "real user prompt", "missing", "scope")
	if !errors.Is(err, gateway.ErrInvalidContinuation) {
		t.Fatalf("expected stale thread: %v", err)
	}
	if _, err := os.Stat(flag); !os.IsNotExist(err) {
		t.Fatal("user prompt was executed in a new thread")
	}
}
