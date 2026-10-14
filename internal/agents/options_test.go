package agents

import (
	"context"
	"encoding/json"
	"feishu-agent-gateway/internal/gateway"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexPerTurnModelAndReasoningArguments(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "codex")
	argsPath := filepath.Join(dir, "args")
	fixture := `#!/bin/sh
if [ "$1" = "app-server" ]; then
 read init; printf '%s\n' '{"id":1,"result":{}}'
 read ready; read check; printf '%s\n' '{"id":2,"result":{"thread":{"id":"thread-1"}}}'
 exit 0
fi
printf '%s\n' "$@" > "` + argsPath + `"
printf '%s\n' '{"type":"thread.started","thread_id":"thread-1"}' '{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}' '{"type":"turn.completed"}'
`
	if err := os.WriteFile(binary, []byte(fixture), 0700); err != nil {
		t.Fatal(err)
	}
	a, err := NewCodex(binary, dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		session   string
		opts      gateway.RunOptions
		wantModel string
	}{{"", gateway.RunOptions{Model: "gpt-6-sol", Reasoning: "high"}, `model="gpt-6-sol"`}, {"thread-1", gateway.RunOptions{Model: "gpt-6-luna", Reasoning: "medium"}, `model="gpt-6-luna"`}} {
		if _, _, err := a.RunWithOptions(context.Background(), "--untrusted", tc.session, "scope", tc.opts); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(argsPath)
		got := string(data)
		if !strings.Contains(got, tc.wantModel) || !strings.Contains(got, `model_reasoning_effort="`+tc.opts.Reasoning+`"`) || !strings.Contains(got, "--\n--untrusted\n") {
			t.Fatalf("bad args: %q", got)
		}
	}
	if strings.Join(a.Models(), ",") != "gpt-6-luna,gpt-6-sol" {
		t.Fatalf("model whitelist = %v; want exactly [gpt-6-luna gpt-6-sol]", a.Models())
	}
}

func TestCodexRejectsModelsOutsideWhitelistBeforeStartingProcess(t *testing.T) {
	for _, model := range []string{"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-6-astra", "--untrusted"} {
		t.Run(model, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, "codex")
			marker := filepath.Join(dir, "started")
			fixture := "#!/bin/sh\ntouch \"" + marker + "\"\nprintf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"thread-1\"}' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"ok\"}}' '{\"type\":\"turn.completed\"}'\n"
			if err := os.WriteFile(binary, []byte(fixture), 0700); err != nil {
				t.Fatal(err)
			}
			a, err := NewCodex(binary, dir, dir)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = a.RunWithOptions(context.Background(), "hello", "", "scope", gateway.RunOptions{Model: model, Reasoning: "high"})
			if err == nil || !strings.Contains(err.Error(), "unsupported Codex model") {
				t.Fatalf("model %q must be rejected; got %v", model, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("unsupported model started subprocess; marker stat: %v", err)
			}
		})
	}
}

func TestCodexEmptyModelPreservesConfiguredDefaultAndHighReasoning(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "codex")
	argsPath := filepath.Join(dir, "args")
	fixture := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsPath + "\"\nprintf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"thread-1\"}' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"ok\"}}' '{\"type\":\"turn.completed\"}'\n"
	if err := os.WriteFile(binary, []byte(fixture), 0700); err != nil {
		t.Fatal(err)
	}
	a, err := NewCodex(binary, dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.RunWithOptions(context.Background(), "hello", "", "scope", gateway.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), "model=\"") || !strings.Contains(string(args), `model_reasoning_effort="high"`) {
		t.Fatalf("empty model must preserve configured default and use high: %q", args)
	}
}

func TestHermesPerTurnSettingsPayload(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, `{"id":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
	}))
	defer srv.Close()
	a, err := NewHermes(srv.URL, "secret", "hermes-agent", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.RunWithOptions(context.Background(), "test", "", "scope", gateway.RunOptions{Model: "hermes-agent", Reasoning: "high"}); err != nil {
		t.Fatal(err)
	}
	if captured["model"] != "hermes-agent" {
		t.Fatalf("model %v", captured["model"])
	}
	opts, ok := captured["model_options"].(map[string]any)
	if !ok || opts["reasoning_effort"] != "high" {
		t.Fatalf("reasoning payload: %v", captured)
	}
}
