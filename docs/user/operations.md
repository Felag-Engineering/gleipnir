# Operations

Day-to-day operational tasks for a running Gleipnir instance.

## Backing up the encryption key

`GLEIPNIR_ENCRYPTION_KEY` is a 32-byte AES-256 master key (stored as 64 hex characters) used to encrypt provider API keys and webhook secrets in the database.

**Losing `GLEIPNIR_ENCRYPTION_KEY` makes all encrypted data in the database permanently unrecoverable. Back it up securely (e.g. a password manager or secrets vault).**

Back it up immediately upon generation, before starting the stack for the first time. Store it in a location separate from your database backups — a single compromise should not expose both the data and the key that unlocks it.

## Rotating the encryption key

Use the `gleipnirctl rotate-key` subcommand to re-encrypt all secrets under a new key in a single atomic transaction. No credentials need to be manually copied out or re-entered.

1. **Generate a new key:**
   ```bash
   openssl rand -hex 32
   ```

2. **Stop the server:**
   ```bash
   docker compose stop gleipnir
   ```

3. **Run the rotation** (pipe keys via stdin to avoid shell history exposure):
   ```bash
   printf '%s\n%s\n' "$OLD_KEY" "$NEW_KEY" | \
     docker compose run --rm gleipnirctl rotate-key --old - --new -
   ```
   On success you'll see something like:
   ```
   re-encrypted 3 provider keys, 1 openai-compat keys, 12 webhook secrets
   ```

4. **Update `GLEIPNIR_ENCRYPTION_KEY`** in your `.env` to the new key.

5. **Bring the server back up:**
   ```bash
   docker compose up -d gleipnir
   ```

Before committing to a live rotation, use `--dry-run` to validate that the old key decrypts every ciphertext without writing anything:

```bash
printf '%s\n%s\n' "$OLD_KEY" "$NEW_KEY" | \
  docker run --rm -i \
    -v gleipnir_data:/data \
    felagengineering/gleipnirctl:latest \
    rotate-key --old - --new - --dry-run
```

See [`cmd/rotatekey/README.md`](../../cmd/rotatekey/README.md) for the full flag reference and security notes.

## Backing up the database

The SQLite database lives at the path set by `GLEIPNIR_DB_PATH` (default: `/data/gleipnir.db`) inside the `gleipnir_data` Docker volume.

WAL mode means the database is spread across up to three files at any moment: the main `.db` file, a `.db-wal` write-ahead log, and a `.db-shm` shared-memory index. A raw file copy taken while the stack is live may capture these files in an inconsistent state, producing a corrupt backup.

**Safe offline backup** (always consistent):

```bash
docker compose stop
# Copy the database file out of the volume while the stack is stopped.
# Adjust the destination path to suit your backup strategy.
docker run --rm \
  -v gleipnir_data:/data \
  -v "$(pwd)":/backup \
  alpine cp /data/gleipnir.db /backup/gleipnir.backup.db
docker compose up -d
```

**Online backup** (no downtime, SQLite handles consistency):

```bash
docker compose exec api sqlite3 /data/gleipnir.db ".backup /data/gleipnir.backup.db"
```

The `.backup` command uses SQLite's built-in online backup API, which is safe to run against a live database. Copy the file out of the volume once the command completes:

```bash
docker run --rm \
  -v gleipnir_data:/data \
  -v "$(pwd)":/backup \
  alpine cp /data/gleipnir.backup.db /backup/gleipnir.backup.db
```

## Upgrading

Pull the latest image and restart the stack. Docker Compose will stop the running container, pull the new image, and start a fresh container against the existing data volume.

```bash
docker compose pull
docker compose up -d
```

The data volume (`gleipnir_data`) is preserved across upgrades. Taking a database backup before upgrading is good practice — see [Backing up the database](#backing-up-the-database) above.

## Environment variable reference

All variables are read at startup. Changing a value requires restarting the stack (`docker compose up -d`).

| Variable | Default | Description |
|---|---|---|
| `GLEIPNIR_ENCRYPTION_KEY` | *(required)* | 64-char hex key (32-byte AES-256) for encrypting provider API keys and webhook secrets. Generate with `openssl rand -hex 32`. |
| `GLEIPNIR_DB_PATH` | `/data/gleipnir.db` | SQLite file path inside the container. |
| `GLEIPNIR_LISTEN_ADDR` | `:8080` | Internal HTTP listen address for the Go server. |
| `GLEIPNIR_LOG_LEVEL` | `info` | Log verbosity: `debug`, `info`, `warn`, or `error`. |
| `GLEIPNIR_MCP_TIMEOUT` | `30s` | Timeout for individual MCP server calls. |
| `GLEIPNIR_HTTP_READ_TIMEOUT` | `15s` | HTTP server read timeout. |
| `GLEIPNIR_HTTP_WRITE_TIMEOUT` | `15s` | HTTP server write timeout. |
| `GLEIPNIR_HTTP_IDLE_TIMEOUT` | `60s` | HTTP server idle timeout. |
| `GLEIPNIR_APPROVAL_SCAN_INTERVAL` | `30s` | How often to check for timed-out approval requests. |
| `GLEIPNIR_DEFAULT_FEEDBACK_TIMEOUT` | `30m` | Default timeout for feedback requests when not set in the policy. |
| `GLEIPNIR_FEEDBACK_SCAN_INTERVAL` | `30s` | How often to check for timed-out feedback requests. |

`GLEIPNIR_PORT` is a Docker Compose variable (not read by the Go server directly). It controls which host port the container exposes and defaults to `3000`.

## Viewing structured logs

Stream live logs from the API container:

```bash
docker compose logs -f api
```

Logs are emitted as JSON by `slog.NewJSONHandler`. Pipe through `jq` for readable output:

```bash
docker compose logs api | jq .
```

Key fields in every log line:

| Field | Description |
|---|---|
| `time` | RFC 3339 timestamp |
| `level` | `DEBUG`, `INFO`, `WARN`, or `ERROR` |
| `msg` | Human-readable event description |
| `run_id` | Present on all log lines tied to a specific run |
| `err` | Error string, present only on `WARN`/`ERROR` lines |

Filter to a single run:

```bash
docker compose logs api | jq 'select(.run_id == "<run_id>")'
```

## Resetting a stuck run

On restart, Gleipnir automatically marks any run in `running`, `waiting_for_approval`, or `waiting_for_feedback` as `interrupted`. This handles the common case of a clean restart after a crash or deployment.

If a run is genuinely stuck — for example, after a manual DB edit left it in an inconsistent state — it can be reset directly with a SQL update:

```bash
docker compose exec api sqlite3 /data/gleipnir.db \
  "UPDATE runs SET status = 'failed', error = 'manually reset' WHERE id = '<run_id>';"
```

**Warning:** This bypasses the normal state machine entirely. The run will be recorded as `failed` with no additional audit steps written. Only use this for runs that are truly stuck and will not recover on their own. Always verify the run ID before executing — there is no confirmation prompt.
