package agents

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCodexThreadCheckKeepsInputOpenUntilResponse(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python fixture unavailable")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "fake-codex")
	// Model the real app-server: closing input cancels pending work, so the
	// client must wait for response id=2 before closing or killing the server.
	script := `#!/usr/bin/env python3
import json, sys, select
if sys.argv[1] == "app-server":
    req=json.loads(sys.stdin.readline())
    print(json.dumps({"id":req["id"],"result":{}}),flush=True)
    assert json.loads(sys.stdin.readline())["method"] == "initialized"
    req=json.loads(sys.stdin.readline())
    if select.select([sys.stdin],[],[],0.1)[0] and not sys.stdin.read(1):
        sys.exit(0)
    print(json.dumps({"id":req["id"],"result":{"thread":{"id":"thread-1"}}}),flush=True)
    sys.stdin.read()
else:
    print('{"type":"thread.started","thread_id":"thread-1"}')
    print('{"type":"item.completed","item":{"type":"agent_message","text":"continued"}}')
    print('{"type":"turn.completed"}')
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a, err := NewCodex(binary, dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	answer, next, err := a.Run(context.Background(), "continue", "thread-1", "scope")
	if err != nil || answer != "continued" || next != "thread-1" {
		t.Fatalf("continuation: %q %q %v", answer, next, err)
	}
}

func TestCodexRecoveredRetryErrorIsNotFinalFailure(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "fake-codex")
	script := `#!/bin/sh
printf '%s\n' '{"type":"thread.started","thread_id":"thread-1"}' '{"type":"error","message":"Reconnecting... 1/5"}' '{"type":"item.completed","item":{"type":"agent_message","text":"recovered"}}' '{"type":"turn.completed"}'
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a, _ := NewCodex(binary, dir, dir)
	answer, _, err := a.Run(context.Background(), "test", "", "scope")
	if err != nil || answer != "recovered" {
		t.Fatalf("completed turn was discarded: %q %v", answer, err)
	}
}

func TestCodexOutputLimitsTerminateWriterBeforeDeadline(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python fixture unavailable")
	}
	for _, tc := range []struct{ name, body string }{
		{"oversized line", `sys.stdout.write("x" * (2 << 20)); sys.stdout.flush()`},
		{"total budget", `sys.stdout.write(json.dumps({"type":"progress","padding":"x" * (768 << 10)})+"\n"); sys.stdout.flush()`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, "fake-codex")
			script := "#!/usr/bin/env python3\nimport sys,json\nfor _ in range(100):\n    " + tc.body + "\n"
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			a, _ := NewCodex(binary, dir, dir)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _, err := a.Run(ctx, "test", "", "scope")
			if err == nil {
				t.Fatal("oversized output must fail closed")
			}
			if ctx.Err() != nil {
				t.Fatalf("wait blocked on a writer after scanner stopped: %v", err)
			}
		})
	}
}

func TestCodexChildDoesNotInheritGatewaySecrets(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	observed := filepath.Join(dir, "child-env")
	t.Setenv("FEISHU_APP_SECRET", "feishu-do-not-leak")
	t.Setenv("HERMES_API_KEY", "hermes-do-not-leak")
	t.Setenv("OPENAI_API_KEY", "openai-do-not-leak")
	t.Setenv("OTHER_SERVICE_PASSWORD", "misc-do-not-leak")
	t.Setenv("GATEWAY_MARKER", "not-for-backends")
	code := `#!/bin/sh
if [ "$1" = "app-server" ]; then
  printf '%s\n' "$FEISHU_APP_SECRET|$HERMES_API_KEY|$OPENAI_API_KEY|$OTHER_SERVICE_PASSWORD|$GATEWAY_MARKER|$CODEX_HOME" >> "` + observed + `"
  read init; printf '%s\n' '{"id":1,"result":{}}'
  read ready; read check; printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-1"}}}'
  exit 0
fi
printf '%s\n' "$FEISHU_APP_SECRET|$HERMES_API_KEY|$OPENAI_API_KEY|$OTHER_SERVICE_PASSWORD|$GATEWAY_MARKER|$CODEX_HOME" >> "` + observed + `"
printf '%s\n' '{"type":"thread.started","thread_id":"thread-1"}' '{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}' '{"type":"turn.completed"}'
`
	if err := os.WriteFile(script, []byte(code), 0700); err != nil {
		t.Fatal(err)
	}
	a, err := NewCodex(script, dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = a.Run(context.Background(), "first", "", "scope"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = a.Run(context.Background(), "second", "thread-1", "scope"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(observed)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "|||||"+dir {
			t.Fatalf("Codex child received gateway secret or lost its private home: %q", line)
		}
	}
}

func TestCodexRejectsUnsafeConfiguredExecutionUID(t *testing.T) {
	for _, value := range []string{"0", "-1", "root", "4294967296"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CODEX_RUN_UID", value)
			if _, err := NewCodex("codex", t.TempDir(), t.TempDir()); err == nil {
				t.Fatalf("unsafe UID %q accepted", value)
			}
		})
	}
}

func TestCodexChildUsesDedicatedHomeWhenRunningAsOtherUID(t *testing.T) {
	t.Setenv("CODEX_RUN_UID", "1002")
	dir := t.TempDir()
	a, err := NewCodex("codex", dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.childEnv(); !slices.Contains(got, "HOME="+dir) {
		t.Fatalf("child HOME must be private Codex home: %v", got)
	}
}
