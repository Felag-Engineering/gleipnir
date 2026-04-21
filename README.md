# Gleipnir

Gleipnir is a self-hosted runner for AI agents that quietly handle work you'd rather offload, built so you can leave them running and not worry about what they're doing.

## What you can do with it

- **Plan the week's meals.** Checks your calendar for nights without dinner plans, picks recipes for the empty slots, and drops the grocery list into Google Keep. → [Playbook](docs/playbooks/meal-planning.md)
- **Keep your homelab up when hardware fails.** Watches Uptime Kuma for outages, stands the affected service back up on a different host, and updates DNS so users never notice. → [Playbook](docs/playbooks/homelab-failover.md)
- **Weekly budget check-in.** Reviews the week's transactions in Actual Budget, flags anything unusual, and updates category notes. → [Playbook](docs/playbooks/budget-checkin.md)
- **Research your own todo list.** Finds Todoist tasks tagged `research`, gathers context, and updates each task with what it found. → [Playbook](docs/playbooks/todoist-research.md)

## How it stays safe

Every Gleipnir agent runs against an explicit list of tools you grant it. Tools that aren't on the list are never registered with the agent, so they don't exist from its perspective. There is no prompt-based restriction to bypass and no clever phrasing that unlocks something you didn't allow.

Any tool can be marked as requiring approval. When the agent tries to use one, the run pauses, you get a request in the UI showing exactly what the agent wants to do, and nothing happens until you approve or reject it. Every step the agent takes — its thoughts, its tool calls, the results, the approvals — is recorded in a reasoning trace you can read after the fact.

## Quick start

1. Clone the repo and copy the environment template:

   ```bash
   git clone https://github.com/your-org/gleipnir.git
   cd gleipnir
   cp .env.example .env
   ```

2. Start the stack:

   ```bash
   docker compose up -d
   ```

3. Open `http://localhost:3000` in your browser.

4. Complete the first-run setup: create the admin user, then add your Anthropic API key on the `/admin/models` page.

### Back up your encryption key

`GLEIPNIR_ENCRYPTION_KEY` is a mandatory AES-256 key that encrypts every provider API key and webhook secret stored in the database. Losing it makes those credentials permanently unrecoverable — there is no fallback decryption path. Store it in a password manager or secrets vault immediately after generating it. See [Operations — Backing up the encryption key](docs/user/operations.md#backing-up-the-encryption-key).

## Your first policy

Once the stack is running and you're logged in, here's how to wire up your first agent:

1. Go to **Settings → MCP Servers** and click **Add Server**. Give it a name and the URL of any MCP server you have running.
2. Click **Discover** on the new server. Gleipnir reads the server's tool list and shows them in the UI.
3. For each discovered tool, set its tag: `sensor` for read-only tools, `actuator` for tools that change something. Mark anything risky as requiring approval.
4. Go to **Policies → New**. Pick a webhook trigger, write a short task description for the agent, and check the boxes for the tools you just tagged.
5. Click **Trigger** on the new policy to fire a manual run.
6. Open the run from the runs list and watch the trace populate as the agent works.

For fully worked examples against specific services, follow one of the [playbooks](docs/playbooks/).

## More reading

- [Playbooks](docs/playbooks/) — copy-pasteable setups for the use cases above.
- [User docs](docs/user/) — encryption key, backups, logs, troubleshooting.
- [Developer docs](docs/developer/) — architecture, building, contributing.
- [Security notes](SECURITY.md)

## License

[MIT](LICENSE)
