# gleipnirctl

gleipnirctl is the local admin CLI for Gleipnir. It provides direct database-level operations for maintenance and recovery tasks that require the server to be stopped or that need to bypass the web UI.

All commands run as one-off containers against the `api` service (which has the correct volume mounts and environment already configured):

```bash
docker compose run --rm api gleipnirctl <command> [flags]
```

## When to use it vs the web UI

The web UI handles day-to-day operations: managing policies, reviewing runs, approving tool calls, and configuring models. Use gleipnirctl for emergencies and maintenance — recovering a locked-out account, rotating encryption keys, validating backups — where direct database access is needed or the server must be stopped.

## Available commands

| Command | Description |
|---|---|
| `rotate-key` | Re-encrypt all at-rest secrets under a new encryption key |
| `reset-password` | Reset a user's password directly in the database |
| `create-user` | Create a new user with an assigned role directly in the database |
| `list-users` | List all users with roles and status (never prints credentials) |
| `purge-runs` | Delete old terminal runs and their steps (supports `--dry-run`) |
| `verify-keys` | Check that `GLEIPNIR_ENCRYPTION_KEY` decrypts every stored secret (read-only) |
| `check` | Read-only health check: DB, schema, encryption key, stored secrets, admin user |

---

## rotate-key

Re-encrypts every at-rest secret in the Gleipnir database under a new `GLEIPNIR_ENCRYPTION_KEY`, in a single atomic transaction.

### When to use this

- You suspect the current encryption key has been compromised
- You want to rotate the key on a schedule as a security hygiene practice
- You are restoring from a backup and need to re-key the secrets

### What gets rotated

| Location | Column |
|---|---|
| LLM provider API keys | `system_settings` rows matching `*_api_key` |
| OpenAI-compatible backend API keys | `openai_compat_providers.api_key_encrypted` |
| Per-policy webhook secrets | `policies.webhook_secret_encrypted` |
| MCP server auth headers | `mcp_servers.auth_headers_encrypted` |
| Plugin credentials (all strategies, incl. OAuth tokens) | `plugin_instances.credentials_encrypted` |

User passwords and session tokens are **not** affected — they use a separate one-way hash and do not need rotation here.

### Full rotation workflow

Key rotation requires a brief maintenance window. The server must be stopped because the command refuses to run while another process holds the database write lock.

**1. Generate a new key:**

```bash
openssl rand -hex 32
```

**2. Stop the server:**

```bash
docker compose stop api
```

**3. Run the rotation** (`docker compose run` inherits the volume mounts and environment from the `api` service automatically):

```bash
printf '%s\n%s\n' "$OLD_KEY" "$NEW_KEY" | \
  docker compose run --rm api gleipnirctl rotate-key --old - --new -
```

Keys are piped via stdin so they never appear in process listings or shell history. On success you'll see:

```
re-encrypted 3 provider keys, 1 openai-compat keys, 12 webhook secrets, 2 MCP auth header sets, 1 plugin credential sets
```

**4. Update `GLEIPNIR_ENCRYPTION_KEY`** in your `.env` to the new key.

**5. Bring the server back up:**

```bash
docker compose up -d api
```

### Dry run

Before committing to a live rotation, use `--dry-run` to validate that the old key decrypts every ciphertext without writing anything. Useful for verifying a backup is intact:

```bash
printf '%s\n%s\n' "$OLD_KEY" "$NEW_KEY" | \
  docker compose run --rm api gleipnirctl rotate-key --old - --new - --dry-run
```

Output on success:
```
re-encrypted 3 provider keys, 1 openai-compat keys, 12 webhook secrets, 2 MCP auth header sets, 1 plugin credential sets (dry-run; no changes written)
```

### Inline flags (less secure)

Keys passed as flag values are visible in `/proc/<pid>/cmdline` and shell history — the command will warn you when this is detected. Acceptable for local dev; avoid in production:

```bash
docker compose run --rm api gleipnirctl rotate-key --old <current-key> --new <new-key>
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `--old` | *(required)* | Current encryption key. Use `-` to read from stdin. |
| `--new` | *(required)* | New encryption key. Use `-` to read from stdin. |
| `--dry-run` | `false` | Validate decryption and simulate rotation without writing. |
| `--db-path` | `$GLEIPNIR_DB_PATH` or `/data/gleipnir.db` | Path to the SQLite database file. |

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Unexpected error (I/O failure, DB error) |
| 2 | Bad input (invalid key format, equal keys, missing flags) |
| 3 | Database is held by another process — stop the server first |

### Security notes

- **Key material in flags:** When `--old`/`--new` are passed as literal values, both keys are readable from `/proc/<pid>/cmdline` by any process with the same UID on the host, and are saved in shell history. The command emits a warning when this is detected. In production, always use `--old - --new -` and pipe the keys in.
- **Atomicity:** All re-encryption happens in a single SQLite transaction. A crash or error mid-rotation leaves the database unchanged — the old key remains valid.
- **In-memory key lifetime:** Key bytes are zeroed in memory when the command exits. Intermediate plaintext values (decrypted secrets) cannot be zeroed because Go strings are immutable; they are released to the garbage collector on function return.
- **Server must be stopped:** The command probes for DB write-lock contention and refuses with exit code 3 if the server is running. This prevents the running process from caching stale plaintext while the DB holds new-key ciphertexts.

---

## reset-password

Resets a user's password by writing a new bcrypt hash directly to the database. Uses the same bcrypt cost as the server so passwords set via CLI are accepted by the login handler.

### When to use this

- An admin is locked out of the UI and no other admin account exists to reset the password through the settings page
- You need to set a known password on a user account during a recovery procedure

### Full workflow

**With auto-generated password** (recommended):

```bash
docker compose run --rm api gleipnirctl reset-password <username>
```

Example output:
```
generated password: dGhpcyBpcyBhIHRlc3Q
password reset for user alice
```

The generated password is printed to stdout before the confirmation line. Store it immediately — it is shown only once.

**With an explicit password:**

```bash
docker compose run --rm api gleipnirctl reset-password <username> --password <new-password>
```

The server does not need to be stopped. This command performs a single short UPDATE and does not require holding the database write lock across long operations.

### Flags

| Flag / Argument | Default | Description |
|---|---|---|
| `<username>` | *(required positional)* | Username of the account to update |
| `--password` | *(auto-generated if omitted)* | New password. Must be at least 8 characters. |
| `--db-path` | `$GLEIPNIR_DB_PATH` or `/data/gleipnir.db` | Path to the SQLite database file. |

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Unexpected error (I/O failure, DB error, hashing failure) |
| 2 | Bad input (password shorter than 8 characters) |
| 4 | User not found |

### Security notes

- **Generated password is secret material.** It is printed to stdout so it can be captured by downstream tools (`... | tee password.txt`). Do not share terminal output containing this line.
- **Rotate via the UI after logging in.** Once access is restored, change the password through the settings page so it is set according to your organization's password policy.
- **Deactivated users.** This command resets the password hash only. It does not reactivate a deactivated account. Use the web UI (admin role) to reactivate a user.

---

## create-user

Creates a new Gleipnir user with the specified role, writing the account directly to the database. Useful for bootstrapping a second admin account or automating user provisioning without going through the web UI.

### When to use this

- You need a second admin account and have only database access (no working admin session in the UI)
- You are provisioning users in a CI/CD pipeline or automated setup script

### Full workflow

**With auto-generated password** (recommended):

```bash
docker compose run --rm api gleipnirctl create-user <username> --role admin
```

Example output:
```
generated password: dGhpcyBpcyBhIHRlc3Q
created user alice with role admin
```

The generated password is printed to stdout before the confirmation line. Store it immediately — it is shown only once.

**With an explicit password:**

```bash
docker compose run --rm api gleipnirctl create-user <username> --role operator --password <password>
```

The server does not need to be stopped. This command performs a short INSERT that does not require holding the database write lock across long operations.

### Flags

| Flag / Argument | Default | Description |
|---|---|---|
| `<username>` | *(required positional)* | Username for the new account |
| `--role` | `operator` | Role to assign (`admin`, `operator`, `approver`, `auditor`) |
| `--password` | *(auto-generated if omitted)* | Password for the new account. Must be at least 8 characters. |
| `--db-path` | `$GLEIPNIR_DB_PATH` or `/data/gleipnir.db` | Path to the SQLite database file. |

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Unexpected error (I/O failure, DB error, hashing failure) |
| 2 | Bad input (invalid role, password shorter than 8 characters) |
| 4 | Username already exists |

### Security notes

- **Generated password is secret material.** It is printed to stdout so it can be captured by downstream tools (`... | tee password.txt`). Do not share terminal output containing this line.
- **Role validation happens before the database is opened.** Supplying an unrecognised role exits with code 2 without writing anything to the database.

---

## list-users

Prints a table of all users read directly from the database: username, roles, creation time, and status.

```bash
docker compose run --rm api gleipnirctl list-users
```

Example output:
```
USERNAME  ROLES           CREATED_AT            STATUS
alice     admin,operator  2026-01-02T03:04:05Z  active
bob       auditor         2026-01-03T03:04:05Z  deactivated
```

- Rows are ordered by username.
- A user holds one or more roles; they are printed comma-separated in alphabetical order (`-` if the user has none).
- `STATUS` is `deactivated` when the account has been deactivated, otherwise `active`. Deactivated users are included.
- With no users, the header is printed followed by a `no users` line.
- Password hashes and other credential material are never read or printed.

The server does not need to be stopped; the command only reads.

### Flags

| Flag | Default | Description |
|---|---|---|
| `--db-path` | `$GLEIPNIR_DB_PATH` or `/data/gleipnir.db` | Path to the SQLite database file. |

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success (including an empty user table) |
| 1 | Unexpected error (I/O failure, DB error) |

---

## purge-runs

Deletes old terminal runs and their steps directly from the database, to keep it from growing without bound.

```bash
# See what would go, then do it
docker compose run --rm api gleipnirctl purge-runs --older-than 90d --dry-run
docker compose run --rm api gleipnirctl purge-runs --older-than 90d
```

Output:
```
dry run: would delete 120 runs and 3401 steps older than 90d
deleted 120 runs and 3401 steps older than 90d
```

- **`--older-than` is required.** It takes a Go duration (`36h`, `90m`) or a whole number of days (`90d`). Zero, negative and fractional-day values are rejected.
- **What "older" means:** a run's age is measured from `completed_at`, falling back to `started_at` when no completion time was recorded (for example runs marked `interrupted` by a restart). A long-running run that finished recently is therefore not purged.
- **Only terminal runs are deleted:** `complete`, `failed` and `interrupted` by default. `--status` (comma-separated) narrows the set. Naming `pending`, `running`, `waiting_for_approval` or `waiting_for_feedback` is an error and nothing is deleted; active runs are never touched, however old.
- **Dependent rows:** steps, approval requests, feedback requests, tool-input requests, MCP tasks and pending plugin channel requests are removed with the run (`ON DELETE CASCADE`). The reported step count is `run_steps` only.
- **Plugin audit events are kept.** They are security and oversight records (including tool-initiated HITL decision records). Their `run_id` is set to `NULL` when the run goes; the events themselves are not deleted.
- The purge is a single write transaction: it either fully applies or not at all. The server does not need to be stopped; the command waits up to 10 seconds for the write lock. A dry run takes no write lock.

### Flags

| Flag | Default | Description |
|---|---|---|
| `--older-than` | *(required)* | Age threshold: Go duration or whole days such as `90d`. |
| `--status` | `complete,failed,interrupted` | Comma-separated terminal statuses to purge. |
| `--dry-run` | `false` | Print counts without deleting anything. |
| `--db-path` | `$GLEIPNIR_DB_PATH` or `/data/gleipnir.db` | Path to the SQLite database file. |

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success (including nothing to purge) |
| 1 | Invalid flags, or an I/O or DB error (nothing is deleted) |

---

## verify-keys

Checks that the key in `GLEIPNIR_ENCRYPTION_KEY` decrypts every at-rest secret in the database — the same set of columns `rotate-key` re-encrypts (both read one shared list, so they cannot drift apart). Run it after a restore, a key rotation, or before a risky change.

```bash
docker compose run --rm api gleipnirctl verify-keys
```

Success:
```
verified 12 secrets OK
```

Failure lists each row that did not decrypt, by table, column and row identifier (never plaintext or ciphertext):
```
FAILED mcp_servers.auth_headers_encrypted id=01J...
error: 1 secrets failed to decrypt (11 verified OK)
```

- The key is read from the environment only; there is deliberately no `--key` flag, so it cannot leak into shell history or process listings. It may be hex or base64.
- The database is opened read-only and is never migrated or created, so the server does not need to be stopped.
- A database with no stored secrets verifies `0 secrets OK` and exits 0.

### Flags

| Flag | Default | Description |
|---|---|---|
| `--db-path` | `$GLEIPNIR_DB_PATH` or `/data/gleipnir.db` | Path to the SQLite database file. |

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Every secret decrypted |
| 1 | One or more secrets failed, `GLEIPNIR_ENCRYPTION_KEY` missing or invalid, or an unexpected error |

---

## check

Runs read-only health checks and prints one line per check.

```bash
docker compose run --rm api gleipnirctl check
```

Example output:
```
PASS  encryption key present and valid
PASS  database reachable: /data/gleipnir.db
PASS  schema migrated
PASS  encryption key decrypts stored secrets: 12 secrets
WARN  active admin user: none found; create one with gleipnirctl create-user
```

| Check | PASS when | Otherwise |
|---|---|---|
| database reachable | the file exists and a trivial query succeeds | FAIL |
| schema migrated | no registered migration is pending | FAIL |
| encryption key present and valid | `GLEIPNIR_ENCRYPTION_KEY` is set and is a 32-byte hex or base64 key | FAIL |
| encryption key decrypts stored secrets | every secret `verify-keys` covers decrypts | FAIL, with one line per undecryptable row |
| active admin user | at least one non-deactivated user holds the `admin` role | WARN |

- A check that cannot run because one it depends on failed (for example, secrets when the key is missing) prints `SKIP`; the root cause carries the failure.
- The secrets check decrypts every stored secret rather than a sample, so a single corrupt row is always found.
- No admin user is a `WARN`, not a failure: the instance is otherwise healthy, and a fresh install has no admin until first-run setup. Warnings do not affect the exit code.
- Nothing is written. The database is opened read-only and is never migrated or created, so a pending migration is reported, not applied, and the server does not need to be stopped. Migrations that only fix up data and have no schema probe cannot be detected as pending.

### Flags

| Flag | Default | Description |
|---|---|---|
| `--db-path` | `$GLEIPNIR_DB_PATH` or `/data/gleipnir.db` | Path to the SQLite database file. |

### Exit codes

| Code | Meaning |
|---|---|
| 0 | No check failed (warnings allowed) |
| 1 | At least one check failed |
