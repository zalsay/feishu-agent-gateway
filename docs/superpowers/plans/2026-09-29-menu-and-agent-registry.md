# Native Menu and Configured Agent Registry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add native Feishu menu operations and configuration-only registration of supported Agent instances.

**Architecture:** Parse authenticated menu events into existing gateway control commands. Resolve their private chat without changing durable session keys. Build backend instances from optional strict JSON and env references while retaining legacy startup.

**Tech Stack:** Go 1.25, existing official Feishu SDK and standard-library JSON.

**Spec:** `docs/superpowers/specs/2026-09-29-menu-and-agent-registry-design.md`

## Global Constraints
- Allowlist must be checked before menu operations; menus do not authorize new users.
- Native event keys: agent_<alias>, agents_list, session_reset, gateway_help.
- No secrets in configuration JSON and no paid network calls in ordinary tests.
- Existing state-file keys and public Agent.Run contract remain unchanged.

### Task 1: Registry (independent worker)
Files: `internal/registry/registry.go`, `internal/registry/registry_test.go`.
Interface: `Load(path string, getenv func(string) string) (map[string]gateway.Agent, error)`.
- [x] Write failing strict-config and legacy-env tests, implement constructors for codex/hermes and validate env references.
- [x] Run focused tests to GREEN; preserve path/name/type isolation and fail startup on malformed config.

### Task 2: Menu parsing and safe private-chat resolution
Files: `internal/feishu/menu.go`, `internal/feishu/menu_test.go`, `internal/gateway/gateway.go`, `internal/gateway/gateway_test.go`.
Interfaces: `ParseMenu(*larkapplication.P2BotMenuV6, map[string]bool) (MenuAction,error)`; `Gateway.ChatIDForUser(string) (string,bool)`.
- [x] Test normal/unknown/malformed events and multiple conversation ambiguity to RED.
- [x] Implement known-key translation and unique existing-chat lookup; preserve session keys.
- [x] Tests to GREEN.

### Task 3: Runtime wiring and setup artifacts
Files: `cmd/gateway/main.go`, `cmd/gateway/menu.go`, `cmd/gateway/menu_test.go`, `cmd/gateway/main_test.go`, `agents.example.json`, `README.md`, `.env.example`.
- [x] Write runtime tests for authorization, bootstrap chat, dedup and menu-command behavior.
- [x] Wire OnP2BotMenuV6, shared bounded worker admission and configurable registry; keep old environment startup supported.
- [x] Document adding instances vs new protocols and static native-menu expansion.
- [x] Run go test ./..., go test -race ./..., go vet ./..., go build ./... and independent review.
- [x] Configure application menu/event through Feishu UI and verify saved state; report pending publish/user-click verification separately.
