# Feishu Agent Gateway — MVP Design

One Feishu application bot accepts direct text messages and routes them to Codex or Hermes. This is *text alias* routing, not native Feishu mentions of multiple bots.

- Transport: official Go SDK WebSocket EventDispatcher for inbound (no hidden batching) and Channel API for outbound; no public inbound callback. Ignore group messages and reject non-text private messages. A fixed allowlist of Feishu sender IDs is mandatory.
- UX: `/agents`, `/use codex|hermes`, `@codex ...`, `@hermes ...`, `/reset`, `/help`. Explicit aliases override the per-conversation selection. No automatic intent routing. Reply with an agent label.
- Core: isolate state by chat ID + sender ID, and agent; store selection, backend continuation IDs and a bounded list of completed event IDs in an atomic JSON file. At most one in-flight turn per conversation. Ignore duplicate event IDs; interrupted/retried deliveries may be re-run after a crash (at-least-once, not exactly-once).
- Codex: use a dedicated GATEWAY_CODEX_HOME, preflight thread/read, then invoke locally installed `codex exec --json` (or `codex exec resume ID --json`) in a fixed server-controlled Git repository, with read-only sandbox for new turns. Do not accept cwd or flags from chat. Fail closed on unsuccessful turns.
- Hermes: HTTPS for remote targets; stable hashed X-Hermes-Session-Key, preflight GET for continuation; authenticated HTTP `POST /v1/responses`, `store: true`, `previous_response_id` for continued conversation; only completed responses advance state. No tool approval bridging in MVP; configure Hermes tool permissions accordingly.
- Safety: allowlisted senders only; env-based secrets; bounded concurrent work and per-turn timeout; no message body or credential logging. No group, cards, attachments, streaming, interactive approval, distributed multi-instance deployment or admin UI in MVP.
- Validation: unit tests for routing, isolation, deduplication, persistence and adapters; `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./...`. Live Feishu/Codex/Hermes requires operator credentials and services.
