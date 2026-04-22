# Setup

Getting from zero to a running Gleipnir instance with its first agent.

## Prerequisites

- Docker and Docker Compose v2 (`docker compose version`)
- `openssl` (available on macOS and most Linux distributions by default)

## Option A: Pre-built image (no clone required)

Create a working directory and add two files:

**`docker-compose.yml`**
```yaml
services:
  api:
    image: felagengineering/gleipnir:latest
    ports:
      - "${GLEIPNIR_PORT:-3000}:8080"
    environment:
      - GLEIPNIR_DB_PATH=${GLEIPNIR_DB_PATH:-/data/gleipnir.db}
      - GLEIPNIR_ENCRYPTION_KEY=${GLEIPNIR_ENCRYPTION_KEY:?Set GLEIPNIR_ENCRYPTION_KEY in .env}
    volumes:
      - gleipnir_data:/data
    restart: unless-stopped

volumes:
  gleipnir_data:
```

**`.env`**
```
GLEIPNIR_ENCRYPTION_KEY=<paste-64-hex-chars-here>
GLEIPNIR_PORT=3000
```

Generate the key and paste it into `.env` before starting:

```bash
openssl rand -hex 32
```

**Back this value up to a password manager or secrets vault immediately.** Losing the key makes every stored credential permanently unrecoverable. See [Operations — Backing up the encryption key](operations.md#backing-up-the-encryption-key).

Then skip to [Start the stack](#start-the-stack) below.

## Option B: Build from source

```bash
git clone https://github.com/Felag-Engineering/gleipnir.git
cd gleipnir
cp .env.example .env
```

## Generate the encryption key

Gleipnir encrypts all provider API keys and webhook secrets using AES-256-GCM. The key is required before the stack will start.

```bash
openssl rand -hex 32
```

Open `.env` and paste the output as the value of `GLEIPNIR_ENCRYPTION_KEY`:

```
GLEIPNIR_ENCRYPTION_KEY=<paste-64-hex-chars-here>
```

**Back this value up to a password manager or secrets vault immediately.** Losing the key makes every stored credential permanently unrecoverable — there is no fallback decryption path. See [Operations — Backing up the encryption key](operations.md#backing-up-the-encryption-key).

## Start the stack

```bash
docker compose up -d
```

The container will be healthy once the `/api/v1/health` endpoint responds. You can check:

```bash
docker compose ps
```

## Complete the setup wizard

Open `http://localhost:3000` in your browser. On first run, Gleipnir redirects to the setup wizard. If it doesn't, navigate directly to `http://localhost:3000/setup`. The setup page is only reachable until the first admin account is created — after that it redirects to the login page.

1. **Create the admin account.** Choose a username and a strong password. This is the only account until you add more users.
2. Log in with the credentials you just created.

## Add an LLM provider

Gleipnir needs at least one LLM provider before it can run agents.

1. Go to **Admin → Models** (`/admin/models`).
2. Click **Add provider** and enter your API key for the provider you want to use (Anthropic, Google, or an OpenAI-compatible endpoint).
3. Click **Refresh** to fetch the available models from that provider.
4. Enable the models you want agents to use.

## Set the public URL

If your Gleipnir instance will be accessible from the internet or from other machines on your network, set the public URL so webhook URLs shown in the UI are correct.

1. Go to **Admin → System** (`/admin/system`).
2. Set **Public URL** to the externally reachable base URL, e.g. `https://gleipnir.example.com`. No trailing slash.

If this is a local-only instance, you can skip this step.

## Add an MCP server

Gleipnir calls external tools over MCP (Model Context Protocol). You need at least one MCP server configured before creating a useful policy.

1. Go to **Tools** (`/tools`).
2. Click **Add Server**, give it a name, and enter the HTTP URL of your MCP server (e.g. `http://my-mcp-server:8000`). Note: `localhost` inside the container refers to the Gleipnir container itself — use a hostname or the Docker network alias for servers running on the host.
3. Click **Discover** to fetch its tool list.
4. For each tool, set the tag: `sensor` for read-only tools, `actuator` for tools that change state. The tags inform the approval workflow and operator review — label tools accurately.
5. Mark any tool that requires human confirmation as requiring **Approval**.

## Create and verify your first policy

1. Go to **Agents → New** (`/agents/new`).
2. Set the trigger to **Manual** — no external event source needed to test.
3. Write a short task description in the **Task** field.
4. Under **Capabilities**, check at least one tool from the MCP server you just added.
5. Save the policy, then click **Trigger** to fire a manual run.
6. Open the run from the runs list and watch the reasoning trace populate.

If the run completes, the stack is working end to end.

## Next steps

- [Policies](policies.md) — trigger types, capability grants, run states
- [Roles](roles.md) — add more users with appropriate permissions
- [Operations](operations.md) — database backups, key management, upgrading
- [Playbooks](../playbooks/) — worked examples for common use cases
