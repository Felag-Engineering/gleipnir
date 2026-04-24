# gleipnirctl

gleipnirctl is the local admin CLI for Gleipnir. It provides direct database-level operations for maintenance and recovery tasks that require the server to be stopped or that need to bypass the web UI.

All commands run as one-off containers against the `api` service (which has the correct volume mounts and environment already configured):

```bash
docker compose run --rm api gleipnirctl <command> [flags]
```

## When to use it vs the web UI

The web UI handles day-to-day operations: managing policies, reviewing runs, approving tool calls, and configuring models. Use gleipnirctl for emergencies and maintenance — recovering a locked-out account, rotating encryption keys, validating backups — where direct database access is needed or the server must be stopped.

## Available commands

| Command | Status | Description |
|---|---|---|
| `rotate-key` | Available | Re-encrypt all at-rest secrets under a new encryption key |
| `reset-password` | Available | Reset a user's password directly in the database |
| `create-user` | Coming soon | Create a new user account without going through the web UI |
| `list-users` | Coming soon | List all user accounts and their roles |
| `purge-runs` | Coming soon | Delete run history older than a given date |
| `verify-keys` | Coming soon | Verify that the current encryption key decrypts all stored secrets |
| `check` | Coming soon | Run a health check against the database and configuration |

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
re-encrypted 3 provider keys, 1 openai-compat keys, 12 webhook secrets
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
re-encrypted 3 provider keys, 1 openai-compat keys, 12 webhook secrets (dry-run; no changes written)
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
