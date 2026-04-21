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
| `GLEIPNIR_DEFAULT_PROVIDER` | `anthropic` | Default LLM provider |

**Provider API keys** are not configured via environment variables. They are set through the admin UI at `/admin/models` and stored encrypted in the database. Env vars like `ANTHROPIC_API_KEY` / `GOOGLE_API_KEY` / `OPENAI_API_KEY` are intentionally ignored — a startup warning is logged if they are set.
