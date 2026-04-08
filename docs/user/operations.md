# Operations

Day-to-day operational tasks for a running Gleipnir instance.

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

The `.backup` command uses SQLite's built-in online backup API, which is safe to run against a live database. Copy `/data/gleipnir.backup.db` out of the volume once the command completes.

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

On restart, Gleipnir automatically marks any run in `running` or `waiting_for_approval` as `interrupted`. This handles the common case of a clean restart after a crash or deployment.

If a run is genuinely stuck — for example, after a manual DB edit left it in an inconsistent state — it can be reset directly with a SQL update:

```bash
docker compose exec api sqlite3 /data/gleipnir.db \
  "UPDATE runs SET status = 'failed', error = 'manually reset' WHERE id = '<run_id>';"
```

**Warning:** This bypasses the normal state machine entirely. The run will be recorded as `failed` with no additional audit steps written. Only use this for runs that are truly stuck and will not recover on their own. Always verify the run ID before executing — there is no confirmation prompt.
