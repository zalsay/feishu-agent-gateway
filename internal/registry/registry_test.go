package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"feishu-agent-gateway/internal/agents"
	"feishu-agent-gateway/internal/gateway"
)

func writeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agents.json")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertAgentType[T gateway.Agent](t *testing.T, got gateway.Agent) {
	t.Helper()
	if _, ok := got.(T); !ok {
		t.Fatalf("agent type = %T, want %T", got, *new(T))
	}
}

func TestLoadLegacyEnvironmentFallback(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, binDir, "codex")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	repo, home := t.TempDir(), t.TempDir()

	got, err := Load("", env(map[string]string{
		"CODEX_REPO":         repo,
		"GATEWAY_CODEX_HOME": home,
		"HERMES_BASE_URL":    "https://hermes.example.test",
		"HERMES_API_KEY":     "test-secret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d agents, want 2", len(got))
	}
	assertAgentType[*agents.Codex](t, got["codex"])
	assertAgentType[*agents.Hermes](t, got["hermes"])
	if got["codex"] == nil || got["hermes"] == nil {
		t.Fatalf("legacy names missing: %#v", got)
	}
}

func TestLoadConfiguredMultipleInstancesOfSameType(t *testing.T) {
	bin := writeExecutable(t, t.TempDir(), "codex-test")
	repo1, home1, repo2, home2 := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	config := writeConfig(t, `{"agents":[
		{"name":"codex_work","type":"codex","binary_env":"CODEX_TEST_BIN","repo_env":"CODEX_WORK_REPO","home_env":"CODEX_WORK_HOME"},
		{"name":"codex_personal","type":"codex","binary_env":"CODEX_TEST_BIN","repo_env":"CODEX_PERSONAL_REPO","home_env":"CODEX_PERSONAL_HOME"},
		{"name":"hermes_work","type":"hermes","base_url_env":"HERMES_WORK_URL","api_key_env":"HERMES_WORK_KEY","model_env":"HERMES_WORK_MODEL"},
		{"name":"hermes_personal","type":"hermes","base_url_env":"HERMES_PERSONAL_URL","api_key_env":"HERMES_PERSONAL_KEY"}
	]}`)

	got, err := Load(config, env(map[string]string{
		"CODEX_TEST_BIN":      bin,
		"CODEX_WORK_REPO":     repo1,
		"CODEX_WORK_HOME":     home1,
		"CODEX_PERSONAL_REPO": repo2,
		"CODEX_PERSONAL_HOME": home2,
		"HERMES_WORK_URL":     "https://work.example.test",
		"HERMES_WORK_KEY":     "work-secret",
		"HERMES_WORK_MODEL":   "work-model",
		"HERMES_PERSONAL_URL": "https://personal.example.test",
		"HERMES_PERSONAL_KEY": "personal-secret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d agents, want 4", len(got))
	}
	assertAgentType[*agents.Codex](t, got["codex_work"])
	assertAgentType[*agents.Codex](t, got["codex_personal"])
	assertAgentType[*agents.Hermes](t, got["hermes_work"])
	assertAgentType[*agents.Hermes](t, got["hermes_personal"])
	if model := reflect.ValueOf(got["hermes_personal"]).Elem().FieldByName("model").String(); model != "hermes-agent" {
		t.Fatalf("omitted Hermes model = %q, want hermes-agent", model)
	}
}

func TestLoadHermesOnlyConfigDefaultsModel(t *testing.T) {
	config := writeConfig(t, `{"agents":[{"name":"assistant","type":"hermes","base_url_env":"HERMES_URL","api_key_env":"HERMES_KEY"}]}`)
	got, err := Load(config, env(map[string]string{
		"HERMES_URL": "https://hermes.example.test",
		"HERMES_KEY": "test-secret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got agents %#v, want only assistant", got)
	}
	assertAgentType[*agents.Hermes](t, got["assistant"])
	if model := reflect.ValueOf(got["assistant"]).Elem().FieldByName("model").String(); model != "hermes-agent" {
		t.Fatalf("model = %q, want hermes-agent", model)
	}
}

func TestLoadConfiguredCodexDefaultsBinary(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, binDir, "codex")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	config := writeConfig(t, `{"agents":[{"name":"local","type":"codex","repo_env":"REPO","home_env":"HOME_DIR"}]}`)
	got, err := Load(config, env(map[string]string{"REPO": t.TempDir(), "HOME_DIR": t.TempDir()}))
	if err != nil {
		t.Fatal(err)
	}
	assertAgentType[*agents.Codex](t, got["local"])
}

func TestLoadRejectsInvalidConfigurations(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{name: "unknown type", json: `{"agents":[{"name":"assistant","type":"other"}]}`},
		{name: "unknown field and literal key", json: `{"agents":[{"name":"assistant","type":"hermes","api_key":"literal-secret"}]}`},
		{name: "unknown env alias", json: `{"agents":[{"name":"assistant","type":"hermes","base_url_env":"URL","key_env":"KEY"}]}`},
		{name: "invalid name", json: `{"agents":[{"name":"Assistant","type":"hermes"}]}`},
		{name: "duplicate name", json: `{"agents":[{"name":"assistant","type":"hermes"},{"name":"assistant","type":"hermes"}]}`},
		{name: "empty agents", json: `{"agents":[]}`},
		{name: "missing required env reference fields", json: `{"agents":[{"name":"assistant","type":"hermes"}]}`},
		{name: "invalid env name", json: `{"agents":[{"name":"assistant","type":"hermes","base_url_env":"URL","api_key_env":"BAD-KEY"}]}`},
		{name: "non-ASCII env name", json: `{"agents":[{"name":"assistant","type":"hermes","base_url_env":"URL","api_key_env":"KÉY"}]}`},
		{name: "duplicate JSON property", json: `{"agents":[],"agents":[]}`},
		{name: "trailing JSON", json: `{"agents":[]} {}`},
		{name: "non-object root", json: `[]`},
		{name: "noncanonical root field casing", json: `{"AGENTS":[{"name":"assistant","type":"hermes","base_url_env":"URL","api_key_env":"KEY"}]}`},
		{name: "noncanonical agent field casing", json: `{"agents":[{"NAME":"assistant","type":"hermes","base_url_env":"URL","api_key_env":"KEY"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.json), env(nil))
			if err == nil {
				t.Fatal("Load succeeded, want validation error")
			}
			if strings.Contains(err.Error(), "literal-secret") {
				t.Fatalf("error leaked literal credential: %v", err)
			}
		})
	}
}

func TestLoadRejectsOversizedConfig(t *testing.T) {
	content := `{"agents":[]}` + strings.Repeat(" ", (1<<20)+1)
	_, err := Load(writeConfig(t, content), env(nil))
	if err == nil {
		t.Fatal("oversized configuration accepted")
	}
}

func TestLoadRejectsMissingEnvironmentAndUnavailableCodex(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, binDir, "codex")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	repo, home := t.TempDir(), t.TempDir()
	legacy := map[string]string{
		"CODEX_REPO":         repo,
		"GATEWAY_CODEX_HOME": home,
		"HERMES_BASE_URL":    "https://hermes.example.test",
	}
	_, err := Load("", env(legacy))
	if err == nil || !strings.Contains(err.Error(), "HERMES_API_KEY") {
		t.Fatalf("missing Hermes key error = %v", err)
	}

	config := writeConfig(t, `{"agents":[{"name":"assistant","type":"hermes","base_url_env":"URL","api_key_env":"KEY"}]}`)
	_, err = Load(config, env(map[string]string{"URL": "https://hermes.example.test"}))
	if err == nil || !strings.Contains(err.Error(), "KEY") {
		t.Fatalf("missing referenced key error = %v", err)
	}

	config = writeConfig(t, `{"agents":[{"name":"local","type":"codex","binary_env":"BIN","repo_env":"REPO","home_env":"HOME_DIR"}]}`)
	_, err = Load(config, env(map[string]string{"BIN": filepath.Join(t.TempDir(), "not-found"), "REPO": repo, "HOME_DIR": home}))
	if err == nil {
		t.Fatal("unavailable Codex binary accepted")
	}

	config = writeConfig(t, `{"agents":[{"name":"local","type":"codex","binary_env":"BIN","repo_env":"REPO","home_env":"HOME_DIR"}]}`)
	_, err = Load(config, env(map[string]string{"REPO": repo, "HOME_DIR": home}))
	if err == nil || !strings.Contains(err.Error(), "BIN") {
		t.Fatalf("missing explicitly configured binary error = %v", err)
	}

	config = writeConfig(t, `{"agents":[{"name":"assistant","type":"hermes","base_url_env":"URL","api_key_env":"KEY","model_env":"MODEL"}]}`)
	_, err = Load(config, env(map[string]string{"URL": "https://hermes.example.test", "KEY": "test-secret"}))
	if err == nil || !strings.Contains(err.Error(), "MODEL") {
		t.Fatalf("missing explicitly configured model error = %v", err)
	}
}

func TestLoadErrorsNeverIncludeHermesCredential(t *testing.T) {
	const secret = "sentinel-do-not-leak"
	values := env(map[string]string{"URL": "http://remote.example.test", "KEY": secret})
	configs := []string{
		`{"agents":[{"name":"assistant","type":"hermes","base_url_env":"URL","api_key_env":"KEY"}]}`,
		`{"agents":[{"name":"` + secret + `","type":"hermes","base_url_env":"URL","api_key_env":"KEY"}]}`,
		`{"agents":[{"name":"assistant","type":"hermes","base_url_env":"URL","api_key_env":"KEY","` + secret + `":"ignored"}]}`,
	}
	for _, raw := range configs {
		_, err := Load(writeConfig(t, raw), values)
		if err == nil {
			t.Fatal("invalid or unsafe Hermes configuration accepted")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked Hermes credential: %v", err)
		}
	}
}

func TestLoadLegacyHermesDefaultsUseHTTPClient(t *testing.T) {
	// Keep the default HTTP client assertion local and side-effect free: no requests are made.
	got, err := Load("", env(map[string]string{
		"CODEX_BIN":          writeExecutable(t, t.TempDir(), "codex-legacy"),
		"CODEX_REPO":         t.TempDir(),
		"GATEWAY_CODEX_HOME": t.TempDir(),
		"HERMES_BASE_URL":    "https://hermes.example.test",
		"HERMES_API_KEY":     "secret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertAgentType[*agents.Hermes](t, got["hermes"])
	if model := reflect.ValueOf(got["hermes"]).Elem().FieldByName("model").String(); model != "hermes-agent" {
		t.Fatalf("legacy Hermes model = %q, want hermes-agent", model)
	}
	if got["hermes"] == nil || reflect.ValueOf(got["hermes"]).Elem().FieldByName("client").IsNil() {
		t.Fatal("Hermes client was not initialized")
	}
}

func TestLoadRejectsNoncanonicalFieldCasing(t *testing.T) {
	values := env(map[string]string{"URL": "https://hermes.example.test", "KEY": "test-secret"})
	for _, raw := range []string{
		`{"AGENTS":[{"name":"assistant","type":"hermes","base_url_env":"URL","api_key_env":"KEY"}]}`,
		`{"agents":[{"NAME":"assistant","type":"hermes","base_url_env":"URL","api_key_env":"KEY"}]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := Load(writeConfig(t, raw), values); err == nil {
				t.Fatal("noncanonical JSON field casing accepted")
			}
		})
	}
}

func TestConfigOmittedOptionalFieldsDoNotInheritLegacyEnv(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "codex")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	values := map[string]string{"REPO": dir, "HOME": dir, "CODEX_BIN": "/missing/legacy-binary"}
	configPath := filepath.Join(dir, "agents.json")
	body := `{"agents":[{"name":"code","type":"codex","repo_env":"REPO","home_env":"HOME"}]}`
	if err := os.WriteFile(configPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(configPath, func(k string) string { return values[k] }); err != nil {
		t.Fatalf("config inherited legacy CODEX_BIN: %v", err)
	}
}

func TestConfigOmittedHermesModelUsesProtocolDefault(t *testing.T) {
	var observed string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		observed = req.Model
		fmt.Fprint(w, `{"id":"resp-1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
	}))
	defer server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(path, []byte(`{"agents":[{"name":"h","type":"hermes","base_url_env":"BASE","api_key_env":"KEY"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"BASE": server.URL, "KEY": "test-key", "HERMES_MODEL": "legacy-model"}
	instances, err := Load(path, func(k string) string { return values[k] })
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := instances["h"].Run(context.Background(), "test", "", "scope"); err != nil {
		t.Fatal(err)
	}
	if observed != "hermes-agent" {
		t.Fatalf("protocol default was shadowed by legacy env: %q", observed)
	}
}
