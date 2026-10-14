# Server deployment

This guide describes a generic deployment. Keep hostnames, registry names, provider URLs, runtime state, and deployment reports in private operator-managed configuration rather than this public source repository.

## Prerequisites

- A trusted Linux/amd64 host with Docker Compose and a `GO_RUNTIME_IMAGE` containing `/bin/sh`, CA certificates, and support for the statically linked gateway and Codex executables.
- A dedicated service directory (examples below use `/srv/feishu-agent-gateway`).
- A private `.env` containing Feishu credentials and the sender allowlist; never commit it.
- A dedicated Codex home authenticated for the service, with permissions restricted to the configured runtime user.
- A fixed, trusted source snapshot mounted read-only for Codex. Do not let chat input select a repository or executable arguments.

The Compose example runs one Feishu WebSocket consumer. Do not run a second active instance for the same app unless duplicate event delivery is handled deliberately.

## Secrets and runtime boundaries

- Keep `.env`, Codex authentication, persistent state, logs, backups, binaries, provider configuration, and deployment reports outside Git.
- Mount `.env` read-only and restrict it to the service administrator. Do not expose Feishu or other Agent credentials to the Codex subprocess.
- Run the gateway with only the capabilities needed to drop Codex to a dedicated non-root UID. Use a read-only container root, no new privileges, no published inbound ports, and persistent storage only for state and the dedicated Codex home.
- Configure `AGENT_CONFIG_PATH` to include only backends reachable from this host. A menu item alone does not mean that an Agent backend is deployed.
- Set the trusted provider endpoint in private runtime configuration. Avoid hard-coding production endpoints or credentials into the wrapper script or repository.

## Build and stage

From the project root:

```sh
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o .local/gateway-linux-amd64 ./cmd/gateway
```

Transfer the binary and source snapshot to a private staging area on the host. Transfer `.env` through a protected channel only when necessary; set restrictive permissions immediately. Verify checksums before installation. Do not place credentials in shell history or command-line arguments.

Before activation, run the opt-in live Codex smoke tests from the same runtime image and network configuration used by the service. Verify the configured default model, every model offered in the menu, first-turn and continuation behavior, and that the deployed CLI/provider configuration matches the tested configuration.

## Activate and recover

1. Ensure only one gateway instance is consuming the Feishu app events.
2. Back up the current binary and configuration in the private deployment directory.
3. Stop only this Compose service and allow in-flight requests to finish.
4. Atomically replace the binary and reviewed runtime configuration; do not overwrite `.env` or persistent state as part of a binary release.
5. Start the service and verify the container is running and the Feishu WebSocket reports ready.
6. If startup or live checks fail, restore the backed-up binary/configuration and restart only this service.

Container health and WebSocket readiness do not by themselves prove a successful Feishu client-side interaction. Reopen generated settings cards after a server-side whitelist update; static Feishu menus only need republishing when their structure or callback registration changes.
