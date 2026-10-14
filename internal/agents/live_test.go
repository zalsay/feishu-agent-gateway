package agents

import (
	"context"
	"feishu-agent-gateway/internal/gateway"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Live tests are opt-in: ordinary go test never calls paid model endpoints.
func liveConfig(t *testing.T, names ...string) {
	t.Helper()
	if os.Getenv("GATEWAY_LIVE_TEST") != "1" {
		t.Skip("set GATEWAY_LIVE_TEST=1 for real backend smoke tests")
	}
	for _, name := range names {
		if os.Getenv(name) == "" {
			t.Fatalf("missing %s", name)
		}
	}
}

func TestLiveCodex(t *testing.T) {
	liveConfig(t, "CODEX_BIN", "CODEX_REPO", "GATEWAY_CODEX_HOME")
	a, err := NewCodex(os.Getenv("CODEX_BIN"), os.Getenv("CODEX_REPO"), os.Getenv("GATEWAY_CODEX_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	answer, id, err := a.Run(ctx, "Gateway smoke test: remember marker FSGATEWAY-CODEX-742. Do not use tools or inspect files. Reply exactly CODEX_SMOKE_OK.", "", "live-codex")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "CODEX_SMOKE_OK") || id == "" {
		t.Fatalf("first turn: %q, session present=%v", answer, id != "")
	}
	t.Log("first Codex turn succeeded; testing continuation")
	answer, next, err := a.Run(ctx, "What marker did I tell you to remember? Do not use tools. Reply with only the marker.", id, "live-codex")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "FSGATEWAY-CODEX-742") || next != id {
		t.Fatalf("continuation: %q, same session=%v", answer, next == id)
	}
}

func TestLiveHermes(t *testing.T) {
	liveConfig(t, "HERMES_BASE_URL", "HERMES_API_KEY")
	model := os.Getenv("HERMES_MODEL")
	if model == "" {
		model = "hermes-agent"
	}
	a, err := NewHermes(os.Getenv("HERMES_BASE_URL"), os.Getenv("HERMES_API_KEY"), model, &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	answer, id, err := a.Run(ctx, "Gateway smoke test: remember marker FSGATEWAY-HERMES-863. Do not use tools. Reply exactly HERMES_SMOKE_OK.", "", "feishu-live-hermes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "HERMES_SMOKE_OK") || id == "" {
		t.Fatalf("first turn: %q, session present=%v", answer, id != "")
	}
	t.Log("first Hermes turn succeeded; testing continuation")
	answer, next, err := a.Run(ctx, "What marker did I tell you to remember? Do not use tools. Reply with only the marker.", id, "feishu-live-hermes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer, "FSGATEWAY-HERMES-863") || next == "" {
		t.Fatalf("continuation: %q, session present=%v", answer, next != "")
	}
}

func TestLiveCodexSelectedModelAndReasoning(t *testing.T) {
	liveConfig(t, "CODEX_BIN", "CODEX_REPO", "GATEWAY_CODEX_HOME")
	a, err := NewCodex(os.Getenv("CODEX_BIN"), os.Getenv("CODEX_REPO"), os.Getenv("GATEWAY_CODEX_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"gpt-6-luna", "gpt-6-sol"} {
		t.Run(model, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			selected := gateway.RunOptions{Model: model, Reasoning: "high"}
			text, id, err := a.RunWithOptions(ctx, "Do not use tools. Remember marker FSGATEWAY-MODEL-684. Reply exactly MODEL_MENU_OK.", "", "live-menu-"+model, selected)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(text, "MODEL_MENU_OK") || id == "" {
				t.Fatalf("first selected-model turn: %q", text)
			}
			selected.Reasoning = "medium"
			text, next, err := a.RunWithOptions(ctx, "Do not use tools. What marker did I tell you to remember? Reply only the marker.", id, "live-menu-"+model, selected)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(text, "FSGATEWAY-MODEL-684") || next != id {
				t.Fatalf("changed reasoning continuation: %q, session retained=%v", text, next == id)
			}
		})
	}
}
