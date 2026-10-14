#!/bin/sh
set -eu
umask 077
set -a
. /app/private/.env
set +a
# Local-Mac Hermes URL cannot be used in the server container. Until a
# server-side Hermes API is provided, advertise only the configured Codex.
unset HERMES_BASE_URL HERMES_API_KEY HERMES_MODEL
export AGENT_CONFIG_PATH=/app/config/agents.json
export CODEX_REPO=/app/repo
export GATEWAY_CODEX_HOME=/app/codex-home
export CODEX_BIN=/app/bin/codex-wrapper
export CODEX_RUN_UID=1002
export GATEWAY_STATE_PATH=/app/state/state.json
exec /app/bin/gateway
