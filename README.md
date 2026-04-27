# Gleipnir

Gleipnir is a self-hosted runner for AI agents that quietly handle work you'd rather offload, built so you can leave them running and not worry about what they're doing.

## What you can do with it

- **Plan the week's meals.** Checks your calendar for nights without dinner plans, picks recipes from Mealie for the empty slots, and schedules them in your meal plan. → [Playbook](docs/playbooks/meal-planning/README.md)
- **Research your own todo list.** Finds Todoist tasks tagged `AI_Assist`, gathers context with web search, and posts the findings as a comment on each task. → [Playbook](docs/playbooks/todoist-research/README.md)
- **Run homelab DevOps from natural language.** Restart Docker containers, fix Proxmox VMs, update Technitium DNS records, or reconfigure Caddy routes — every write is approval-gated. → [Playbook](docs/playbooks/devops/README.md)

## How it stays safe

Every Gleipnir agent runs against an explicit list of tools you grant it. Tools that aren't on the list are never registered with the agent, so they don't exist from its perspective. There is no prompt-based restriction to bypass and no clever phrasing that unlocks something you didn't allow.

Any tool can be marked as requiring approval. When the agent tries to use one, the run pauses, you get a request in the UI showing exactly what the agent wants to do, and nothing happens until you approve or reject it. Every step the agent takes — its thoughts, its tool calls, the results, the approvals — is recorded in a reasoning trace you can read after the fact.

## Quick start

**Option A — pre-built image (no clone required):**

```bash
docker pull felagengineering/gleipnir:latest
```

Create a `docker-compose.yml` and a `.env` file as shown in [Setup](docs/user/setup.md), then jump to step 3 below.

**Option B — build from source:**

1. Clone the repo and copy the environment template:

   ```bash
   git clone https://github.com/Felag-Engineering/gleipnir.git
   cd gleipnir
   cp .env.example .env
   ```

2. Generate an encryption key and add it to `.env`:

   ```bash
   openssl rand -hex 32
   # Paste the output into GLEIPNIR_ENCRYPTION_KEY in .env
   ```

3. Start the stack:

   ```bash
   docker compose up -d
   ```

4. Open `http://localhost:3000` in your browser.

5. Complete the first-run setup: create the admin user, then add your LLM provider API key at **Admin → Models**.

### Back up your encryption key

`GLEIPNIR_ENCRYPTION_KEY` is a mandatory AES-256 key that encrypts every provider API key and webhook secret stored in the database. Losing it makes those credentials permanently unrecoverable — there is no fallback decryption path. Store it in a password manager or secrets vault immediately after generating it. See [Operations — Backing up the encryption key](docs/user/operations.md#backing-up-the-encryption-key).

## Your first agent

Once the stack is running and you're logged in, here's how to wire up your first agent:

1. Go to **Tools** (`/tools`) and click **Add MCP server**. Give it a name and the URL of any MCP server you have running.
2. Click **Discover** on the new server. Gleipnir reads the server's tool list and shows them in the UI. Disable any tool you don't want any agent to use — the kill switch on the Tools page is server-wide.
3. Go to **Agents → New Agent** (`/agents/new`). Pick a webhook (or manual) trigger, write a short task description for the agent, and grant the tools you want the agent to use. Mark any write/destructive tool as requiring approval — this gates that tool behind operator review at runtime.
4. Click **Run now** on the new agent to fire a manual run.
5. Open the run from the **Run History** list and watch the trace populate as the agent works.

For fully worked examples against specific services, follow one of the [playbooks](docs/playbooks/).

## More reading

- [Setup](docs/user/setup.md) — detailed first-run walkthrough.
- [Policies](docs/user/policies.md) — trigger types, capability grants, run states.
- [Roles](docs/user/roles.md) — what each role can and cannot do.
- [Operations](docs/user/operations.md) — upgrading, backups, environment variables.
- [Playbooks](docs/playbooks/) — copy-pasteable setups for the use cases above.
- [Developer docs](docs/developer/) — architecture, building, contributing.
- [CLI commands](cmd/gleipnirctl/README.md) — gleipnirctl local admin CLI (e.g. `rotate-key` for encryption key rotation).
- [Security notes](SECURITY.md)

## License

[Business Source License 1.1](LICENSE). Non-production use is free; production use requires a commercial license from Felag Engineering until the Change Date (2028-04-21), after which this version converts to Apache License 2.0.
