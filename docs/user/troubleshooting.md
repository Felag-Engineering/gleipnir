# Troubleshooting

Common issues will be added here as they come up. If you hit something not listed below, please open an issue.

## First-run failures

### Stack fails to start: `GLEIPNIR_ENCRYPTION_KEY must be set`

Docker Compose will refuse to start the container if `GLEIPNIR_ENCRYPTION_KEY` is empty or missing from the environment. The error appears before any container is created.

**Fix:** Generate a key and add it to `.env`:

```bash
openssl rand -hex 32
# Paste the output into GLEIPNIR_ENCRYPTION_KEY in .env
```

Then re-run `docker compose up -d`. Back the key up to a password manager before doing anything else — see [Operations — Backing up the encryption key](operations.md#backing-up-the-encryption-key).

### Stack starts but runs fail immediately: no LLM provider configured

If no provider API key has been added, any run will fail at its first LLM call. The run will appear in `failed` state and the reasoning trace will contain an error step with a message indicating the provider is unavailable or authentication failed.

**Fix:** Add at least one provider key at **Admin → Models** (`/admin/models`). After saving the key, click **Refresh** to fetch the available model list and enable the models you want to use.

### Run fails with tool discovery error: MCP server unreachable

At run start, Gleipnir re-validates all tool references in the policy against the current MCP registry. If a registered server is unreachable or returns an error, the run fails before the agent starts. The error step will name the server and the HTTP status or connection error.

**Fix:** Check that the MCP server is running and reachable from the Gleipnir container. Test connectivity from the shell:

```bash
docker compose exec api wget -qO- http://<mcp-server-host>:<port>/health
```

If the server URL uses a hostname, verify it resolves inside the container network. If you recently changed the server URL, update the entry at **Settings → MCP Servers** and run **Discover** again.

### Run shows `interrupted` after a restart

On restart, Gleipnir automatically moves any run that was in `running`, `waiting_for_approval`, or `waiting_for_feedback` to `interrupted`. This is expected — the agent process was killed mid-run and the run cannot resume where it left off.

**What to do:** Review the reasoning trace to see what the agent completed before the restart. Re-trigger the policy manually if the work needs to be retried.

## Known issues

### Lost encryption key

If `GLEIPNIR_ENCRYPTION_KEY` is lost, there is no recovery path. The AES-256-GCM ciphertext stored in the database cannot be decrypted without the original key — every provider API key and webhook secret stored before the key was lost is permanently gone.

To get the instance working again, follow the key rotation workaround in [Operations — Rotating the encryption key](operations.md#rotating-the-encryption-key-v10-known-limitation). This requires re-entering all provider API keys and rotating all webhook secrets from scratch.
