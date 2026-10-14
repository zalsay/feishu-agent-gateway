# Native Bot Menu and Agent Registry

Approved scope: replace routine slash-command interaction with Feishu native bot menu; keep text aliases and commands as fallback; make already-supported Agent types easier to register.

## UX and event keys

- `选择 Agent` submenu: `使用 Codex` -> `agent_codex`, `使用 Hermes` -> `agent_hermes`, `查看 Agent 列表` -> `agents_list`.
- `新建会话` -> `session_reset`; clears only the selected Agent's continuation, not stored backend history.
- `帮助` -> `gateway_help`.
- Subscribe to `application.bot.menu_v6` via existing WebSocket EventDispatcher. Never require a public HTTP callback for this feature.
- Menu events carry operator open_id but no chat_id. Reuse the single known private chat from existing durable conversation state; if no unique chat is known, send a short bootstrap message by open_id and use Feishu's returned chat_id. Do not fabricate conversation IDs or alter existing state keys.
- Translate only known menu keys to gateway control commands. Verify allowlist before state lookup or any outbound send. Preserve dedup and per-conversation busy protection. Do not silently enroll users from menu clicks.

## Registry

- Optional `AGENT_CONFIG_PATH` JSON file defines named instances; credentials are environment references, never literal keys.
- Supported adapter types remain `codex` and `hermes`; multiple instances of either type may be added by configuration alone. Unknown types are rejected; a new protocol needs a real Go Agent implementation first.
- Without a config path, retain the existing Codex/Hermes env configuration and names.
- Keep `Agent.Run(ctx, prompt, continuation, scope)` and per-alias session isolation unchanged. No automatic routing, arbitrary shell commands from chat, hot reload or plugin installer.
- New configured aliases appear automatically in `/agents`; use `@alias` or `/use alias`. A matching native submenu event `agent_<alias>` can be configured in Feishu; no promise of unlimited native menu capacity or automatic menu provisioning.

## Verification

Pure tests: menu parsing, unknown events, unauthorized actors, known/unknown chat resolution, command dedup and isolation, strict registry config, env references, duplicate aliases, unsupported types, legacy fallback. No paid providers or real Feishu mutations in default tests. Run test, race, vet, build, independent review; then configure and verify Feishu menu separately. UI publishing is not equivalent to backend event verification.
