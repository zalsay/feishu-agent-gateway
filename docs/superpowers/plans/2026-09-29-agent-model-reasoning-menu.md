# Agent Model and Reasoning Menu Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provide per-user/per-Agent model and reasoning selection from native Feishu menu cards, defaulting reasoning to high.

**Architecture:** Gateway persists validated preferences; adapters consume per-turn options. Feishu menu events render signed-context, whitelisted choice cards; card callbacks validate identity and context before persistent updates. Existing text routing and server deployment remain intact.

**Tech Stack:** Go 1.25, Feishu Go SDK channel + WebSocket callback, static Linux/amd64 gateway container, official Codex CLI and Hermes Responses adapters.

**Spec:** `docs/superpowers/specs/2026-09-29-agent-model-reasoning-menu-design.md`

## Global Constraints
- Reasoning default `high`; first Codex model whitelist `gpt-5.6-luna/sol/terra`; current deployed Hermes unavailable.
- Menus: `选择 Agent`, `参数设置`, `更多`; text routes remain supported.
- Never infer user authorization from card payload alone or disclose the bot secret to Codex.
- Deploy only after unit/race/vet/build and opt-in live model test; final Feishu publish requires action-time confirmation.

### Task 1: Preferences and validated operations
Files: `internal/gateway/store.go`, `internal/gateway/gateway.go`, `internal/gateway/gateway_test.go`.
- [x] Test old JSON state migration, high default, unique user/Agent settings, rejecting unregistered model and effort, persistence/dedup, model-switch resets only its own continuation, effort change leaves continuation.
- [x] Run focused test and capture expected RED.
- [x] Implement a guarded settings setter and read-only selection/catalog accessor; run focused test GREEN.

### Task 2: Backend per-turn settings
Files: `internal/agents/codex.go`, `internal/agents/hermes.go`, `internal/agents/agents_test.go`, `internal/agents/codex_protocol_test.go`, `internal/agents/live_test.go`, `internal/registry/registry.go`.
- [x] Test first/resumed Codex args include explicitly whitelisted model and high/default or selected effort and preserve CLI separator, UID and child env; test Hermes model/options mapping or explicit unsupported error.
- [x] Run focused tests RED, implement minimal Agent.Run options + registration, run focused tests GREEN.
- [x] Opt-in live Codex initial/resume with non-default model, retain no paid calls in ordinary tests.

### Task 3: Feishu menu cards and callbacks
Files: `internal/feishu/menu.go`, `internal/feishu/menu_test.go`, `cmd/gateway/menu.go`, `cmd/gateway/menu_test.go`, `cmd/gateway/main.go`, `cmd/gateway/main_test.go`.
- [x] Test new keys, card JSON, actor allowlist, chat ID, selection freshness, invalid/duplicated action, synchronous toast; run RED.
- [x] Register OnP2CardActionTrigger on existing dispatcher and implement menu→card→persist flow; run GREEN.

### Task 4: Docs and deployment
Files: `README.md`, `deploy/README.md`, `docs/superpowers/specs/2026-09-29-agent-model-reasoning-menu-design.md`, server staged binary.
- [x] Document model whitelist/effort/default, context-reset behavior and feature availability; run tests, race, vet, build, review.
- [x] Stage static Linux gateway and preserve server `.env`/private volumes; restart only `feishu-agent-gateway`; verify WebSocket readiness and preserved state.
- [x] Reconfigure Feishu menu and callback with restricted visibility; publish new version after callback action approval.
- [ ] Verify actual user menu and card clicks end-to-end in the Feishu client.
