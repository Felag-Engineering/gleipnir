# gleipnir rotate-key

One-shot CLI subcommand that re-encrypts every at-rest secret in the Gleipnir database under a new `GLEIPNIR_ENCRYPTION_KEY`, in a single atomic transaction.

## When to use this

- You suspect the current encryption key has been compromised
- You want to rotate the key on a schedule as a security hygiene practice
- You are restoring from a backup and need to re-key the secrets

## What gets rotated

| Location | Column |
|---|---|
| LLM provider API keys | `system_settings` rows matching `*_api_key` |
| OpenAI-compatible backend API keys | `openai_compat_providers.api_key_encrypted` |
| Per-policy webhook secrets | `policies.webhook_secret_encrypted` |

User passwords and session tokens are **not** affected — they use a separate one-way hash and do not need rotation here.

## Full rotation workflow

Key rotation requires a brief maintenance window. The server must be stopped because the command refuses to run while another process holds the database write lock.

**1. Generate a new key:**

```bash
openssl rand -hex 32
```

**2. Stop the server:**

```bash
docker compose stop gleipnir
```

**3. Run the rotation using the same service config** (`docker compose run` inherits the correct volume mounts automatically):

```bash
printf '%s\n%s\n' "$OLD_KEY" "$NEW_KEY" | \
  docker compose run --rm gleipnir rotate-key --old - --new -
```

Keys are piped via stdin so they never appear in process listings or shell history. On success you'll see:

```
re-encrypted 3 provider keys, 1 openai-compat keys, 12 webhook secrets
```

**4. Update `GLEIPNIR_ENCRYPTION_KEY`** in your `.env` to the new key.

**5. Bring the server back up:**

```bash
docker compose up -d gleipnir
```

## Dry run

Before committing to a live rotation, use `--dry-run` to validate that the old key decrypts every ciphertext without writing anything. Useful for verifying a backup is intact:

```bash
printf '%s\n%s\n' "$OLD_KEY" "$NEW_KEY" | \
  docker compose run --rm gleipnir rotate-key --old - --new - --dry-run
```

Output on success:
```
re-encrypted 3 provider keys, 1 openai-compat keys, 12 webhook secrets (dry-run; no changes written)
```

## Inline flags (less secure)

Keys passed as flag values are visible in `/proc/<pid>/cmdline` and shell history — the command will warn you when this is detected. Acceptable for local dev; avoid in production:

```bash
docker compose run --rm gleipnir rotate-key --old <current-key> --new <new-key>
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `--old` | *(required)* | Current encryption key. Use `-` to read from stdin. |
| `--new` | *(required)* | New encryption key. Use `-` to read from stdin. |
| `--dry-run` | `false` | Validate decryption and simulate rotation without writing. |
| `--db-path` | `$GLEIPNIR_DB_PATH` or `/data/gleipnir.db` | Path to the SQLite database file. |

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success |
| 1 | Unexpected error (I/O failure, DB error) |
| 2 | Bad input (invalid key format, equal keys, missing flags) |
| 3 | Database is held by another process — stop the server first |

## Security notes

- **Key material in flags:** When `--old`/`--new` are passed as literal values, both keys are readable from `/proc/<pid>/cmdline` by any process with the same UID on the host, and are saved in shell history. The command emits a warning when this is detected. In production, always use `--old - --new -` and pipe the keys in.
- **Atomicity:** All re-encryption happens in a single SQLite transaction. A crash or error mid-rotation leaves the database unchanged — the old key remains valid.
- **In-memory key lifetime:** Key bytes are zeroed in memory when the command exits. Intermediate plaintext values (decrypted secrets) cannot be zeroed because Go strings are immutable; they are released to the garbage collector on function return.
- **Server must be stopped:** The command probes for DB write-lock contention and refuses with exit code 3 if the server is running. This prevents the running process from caching stale plaintext while the DB holds new-key ciphertexts.
