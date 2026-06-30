# Building Gleipnir

## Prerequisites

- Go (see `go.mod` for the exact version)
- Node.js and npm (for the frontend)
- Docker and Docker Compose (for the full stack)
- [`sqlc`](https://sqlc.dev/) if you plan to regenerate database code

## Backend

```bash
go build ./...           # build
go test ./...            # run all tests
go test ./internal/...   # run only internal package tests
sqlc generate            # regenerate internal/db/ from internal/db/queries/*.sql
docker compose up        # run full stack (Go binary with embedded frontend)
```

The `Makefile` wraps the common targets: `make build` (`go build ./...`), `make test` (`go test -race ./...`), `make lint` (gofmt check + plugin import boundary + staticcheck), and `make proto` (regenerate gRPC stubs). `make help` lists them all.

### Cross-compiling for arm64

The backend is CGO-free (`CGO_ENABLED=0`) and uses pure-Go SQLite
(`modernc.org/sqlite`), so there is no C cross-toolchain to set up — building
for another architecture is just a matter of setting `GOARCH`:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o gleipnir-arm64 .
```

The multi-arch container image is produced the same way: the `Dockerfile`'s
builder stages are pinned to `--platform=$BUILDPLATFORM` and cross-compile to
`$TARGETARCH`, so `docker buildx build --platform linux/amd64,linux/arm64`
builds both variants without emulating the Go toolchain. CI publishes both
(`docker-push-dev` / `docker-push-release` in `.github/workflows/ci.yml`), and
the `backend-tests-arm64` job runs the suite natively on an arm64 runner.

**Plugins** are separate compiled Go binaries the host spawns as subprocesses,
so a plugin's `GOARCH` must match the **host** it will run on. The Slack plugin
Makefile takes the same `GOARCH` override: `make build GOARCH=arm64` (or the
`build-arm64` convenience target).

## Frontend

Run from `frontend/`:

```bash
npm run dev              # Vite dev server (proxies /api → localhost:8080)
npm run build            # TypeScript check + production build
npx vitest run           # run Vitest unit tests
npm run storybook        # Storybook on port 6006
```

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `GLEIPNIR_ENCRYPTION_KEY` | *(required)* | Required. 64-char hex (32-byte AES-256) key that encrypts provider API keys and webhook secrets. Losing it is permanent — see [docs/user/operations.md](../user/operations.md). |
| `GLEIPNIR_DB_PATH` | `/data/gleipnir.db` | SQLite file path |
| `GLEIPNIR_LISTEN_ADDR` | `:8080` | HTTP listen address |
| `GLEIPNIR_LOG_LEVEL` | `info` | Log level (debug, info, warn, error) |
| `GLEIPNIR_MCP_TIMEOUT` | `30s` | Timeout for MCP server calls |
| `GLEIPNIR_HTTP_READ_TIMEOUT` | `15s` | HTTP server read timeout |
| `GLEIPNIR_HTTP_WRITE_TIMEOUT` | `15s` | HTTP server write timeout |
| `GLEIPNIR_HTTP_IDLE_TIMEOUT` | `60s` | HTTP server idle timeout |
| `GLEIPNIR_APPROVAL_SCAN_INTERVAL` | `30s` | How often to check for timed-out approvals |
| `GLEIPNIR_DEFAULT_FEEDBACK_TIMEOUT` | `30m` | Default timeout for feedback requests |
| `GLEIPNIR_FEEDBACK_SCAN_INTERVAL` | `30s` | How often to check for timed-out feedback |
| `GLEIPNIR_PLUGIN_REQUEST_SCAN_INTERVAL` | `30s` | How often to check for timed-out plugin channel requests |
| `GLEIPNIR_DRAIN_TIMEOUT` | `5m` | Graceful-shutdown drain timeout for in-flight runs and background loops |
| `GLEIPNIR_PID_FILE` | `/var/run/gleipnir.pid` | Path the server writes its PID to on startup |
| `GLEIPNIR_ALLOW_UNSIGNED_PLUGINS` | `false` | When `true`, the loader accepts plugins lacking a Minisign signature (see ADR-045 §6) |
| `GLEIPNIR_PLUGINS_DIR` | `/plugins` | Directory watched for plugin tarballs (`.tar.gz`/`.tgz`) |
| `GLEIPNIR_OAUTH_REFRESH_INTERVAL` | `5m` | How often the OAuth refresh scanner runs to refresh plugin OAuth2 tokens |
| `GLEIPNIR_OAUTH_REFRESH_LEAD` | `15m` | Lead-time window before token expiry within which a refresh is triggered |
| `GLEIPNIR_PLUGIN_DEDUP_SWEEP_INTERVAL` | `10m` | How often the dedup sweeper evicts `plugin_event_dedup` rows past the fixed 1-hour window |
| `GLEIPNIR_LLM_RETRY_MAX_ATTEMPTS` | `4` | Total attempts (incl. the first) for a transient LLM API failure; `1` disables retry |
| `GLEIPNIR_LLM_RETRY_INITIAL_BACKOFF` | `1s` | Base wait for the manual retry loop (Google + openaicompat) |
| `GLEIPNIR_LLM_RETRY_MAX_BACKOFF` | `30s` | Ceiling for any single wait in the manual retry loop |

**Provider API keys** are not configured via environment variables. They are set through the admin UI at `/admin/models` and stored encrypted in the database. Env vars like `ANTHROPIC_API_KEY` / `GOOGLE_API_KEY` / `OPENAI_API_KEY` are intentionally ignored — a startup warning is logged if they are set.

The system default LLM model is configured through the admin UI (`PUT /api/v1/admin/settings/default-model`), not an environment variable. There is no `GLEIPNIR_DEFAULT_PROVIDER`.
