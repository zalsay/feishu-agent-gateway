package main

import (
	"errors"
	"strings"
	"testing"

	"feishu-agent-gateway/internal/gateway"
)

func TestLoadConfigRequiresAllSecretsAndAllowlist(t *testing.T) {
	values := map[string]string{"FEISHU_APP_ID": "a", "FEISHU_APP_SECRET": "b", "FEISHU_ALLOWED_USER_IDS": "u1,u2", "HERMES_API_KEY": "key", "HERMES_BASE_URL": "http://127.0.0.1:8642", "CODEX_REPO": "/repo", "GATEWAY_CODEX_HOME": "/isolated"}
	get := func(k string) string { return values[k] }
	cfg, err := loadConfig(get)
	if err != nil || len(cfg.allowed) != 2 {
		t.Fatalf("config %+v %v", cfg, err)
	}
	delete(values, "FEISHU_ALLOWED_USER_IDS")
	if _, err := loadConfig(get); err == nil {
		t.Fatal("must fail closed without allowlist")
	}
}
func TestChunkTextAndMessageFilter(t *testing.T) {
	got := chunkText("甲乙丙丁戊", 2)
	if strings.Join(got, "|") != "甲乙|丙丁|戊" {
		t.Fatalf("chunks %v", got)
	}
	m := gateway.Message{ChatType: "p2p", UserID: "u"}
	if filterMessage(m, "image", map[string]bool{"u": true}) != "仅支持文本消息，请改用文字描述问题。" {
		t.Fatal("must reject unsupported content")
	}
	if filterMessage(m, "text", map[string]bool{"u": true}) != "" {
		t.Fatal("text was rejected")
	}
	if !errors.Is(filterMessageError(m, map[string]bool{}), gateway.ErrUnauthorized) {
		t.Fatal("unauthorized sender")
	}
	m.ChatType = "group"
	if !errors.Is(filterMessageError(m, map[string]bool{"u": true}), gateway.ErrIgnored) {
		t.Fatal("group was accepted")
	}
}

func TestWebSocketHasEventDispatcher(t *testing.T) {
	client := newWSClient("app", "secret")
	if client.EventHandler() == nil {
		t.Fatal("WebSocket client has no event dispatcher; messages will never arrive")
	}
}

func TestLoadConfigSeparatesFeishuAndAgentConfiguration(t *testing.T) {
	values := map[string]string{"FEISHU_APP_ID": "app", "FEISHU_APP_SECRET": "secret", "FEISHU_ALLOWED_USER_IDS": "u", "AGENT_CONFIG_PATH": "/private/agents.json"}
	cfg, err := loadConfig(func(k string) string { return values[k] })
	if err != nil || cfg.agentConfigPath != "/private/agents.json" {
		t.Fatalf("registry config=%+v err=%v", cfg, err)
	}
}
