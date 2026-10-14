# Feishu Agent Gateway Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Build a single Feishu bot that routes private text turns to Codex and Hermes.

**Architecture:** Feishu WebSocket dispatcher normalizes messages; outbound Channel API replies; a tested gateway routes and persists per-user/per-agent state; two isolated backend adapters implement a common Run contract.

**Tech Stack:** Go 1.25, Feishu official Go WebSocket SDK and outbound Channel API, standard-library HTTP/JSON/subprocess.

**Spec:** `docs/superpowers/specs/2026-09-29-feishu-agent-gateway-design.md`

## Global Constraints

- Private text only, mandatory sender allowlist, no public callback.
- Codex repository path is server-controlled and sandbox is read-only.
- No native @ identity or interactive approvals in MVP.

---

### Task 1: Routing and durable state

**Files:** `internal/gateway/gateway_test.go`, `internal/gateway/gateway.go`, `internal/gateway/store.go`

**Interfaces:** `Agent.Run(ctx, prompt, continuation, scope) (answer, nextContinuation string, err error)`; `Gateway.Handle(ctx, Message) (string, error)`; `Store` persists conversation state.

- [x] Write table-driven tests for aliases/commands, selection, isolation, duplicate events, busy state and persistence.
- [x] Run `go test ./internal/gateway` and confirm RED due to missing API.
- [x] Implement minimal routing, allowlist and atomic JSON persistence.
- [x] Run `go test ./internal/gateway` and confirm GREEN.

### Task 2: Backend adapters

**Files:** `internal/agents/agents_test.go`, `internal/agents/codex.go`, `internal/agents/hermes.go`

**Interfaces:** same `Agent.Run` signature; new Codex/HTTP Hermes constructors with fixed configuration.

- [x] Write failing tests for Codex JSONL lifecycle and Hermes HTTP continuation/error handling.
- [x] Run `go test ./internal/agents` and confirm RED.
- [x] Implement adapters, run tests to GREEN.

### Task 3: Runtime and operations

**Files:** `cmd/gateway/main.go`, `README.md`, `.env.example`, `go.mod`, `go.sum`

**Interfaces:** `ch.OnMessage` maps private text to `Gateway.Handle`; `ch.Send` replies in the same conversation.

- [x] Add configuration checks, official SDK WebSocket startup and Channel outbound, bounded dispatch, graceful stop and README setup.
- [x] Run `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./...`; fix failures and document live-test limitation.
