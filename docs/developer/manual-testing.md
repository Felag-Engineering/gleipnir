# Manual Testing

A live environment for manual QA against real external services. Not part of the automated test suite — it exists to verify end-to-end behavior with actual MCP servers, real tool calls, and the full approval flow.

### Prerequisites

- Docker and Docker Compose (already required for normal dev)
- An Anthropic API key (already required)
- A Todoist account. The free tier is sufficient. **Recommendation: use a test or throwaway account** — the QA checklist below tests task deletion and close operations against real data.
- A self-hosted [Mealie](https://docs.mealie.io) instance. Mealie is **not** included in the Gleipnir Docker Compose stack — it must be running separately and reachable from within your Docker network before you start.

### Setup

Add three variables to your `.env` file:

```
TODOIST_API_TOKEN=your_token_here
MEALIE_BASE_URL=http://192.168.1.50:9000
MEALIE_API_KEY=your_mealie_api_key
```

Where to find each value:

- **TODOIST_API_TOKEN** — Todoist → Settings → Integrations → Developer → Your personal API token
- **MEALIE_BASE_URL** — the base URL of your Mealie instance. **Must be reachable from inside Docker.** Use a LAN IP address (e.g. `http://192.168.1.50:9000`) or `host.docker.internal` on Mac/Windows. Do not use `localhost` — that resolves to the container itself, not your host machine.
- **MEALIE_API_KEY** — Mealie UI → click your avatar → Profile → API Tokens → create a new token

### Starting the integration stack

```bash
docker compose --profile integrations up -d
```

Without `--profile integrations`, only the core stack starts (`api`, `ui`, `mcp-test-server`). The Todoist and Mealie MCP containers will not start.

Verify all services are running:

```bash
docker compose ps
docker compose logs todoist-mcp
docker compose logs mealie-mcp
```

Common failure modes:

- **Container exits immediately** — missing or empty env var. Check that `TODOIST_API_TOKEN`, `MEALIE_BASE_URL`, and `MEALIE_API_KEY` are all set in `.env`.
- **Mealie tools fail at runtime** — wrong `MEALIE_BASE_URL` format. Make sure the URL includes the protocol (`http://` or `https://`) and has no trailing slash.

### Registering MCP servers in Gleipnir

1. Open [http://localhost:3000](http://localhost:3000)
2. Go to **Settings → MCP Servers → Add Server**
3. Register Todoist: name `todoist`, URL `http://todoist-mcp:8091/`
4. Register Mealie: name `mealie`, URL `http://mealie-mcp:8092/mcp`
5. Click **Discover** on each server after registering

### Tagging capabilities

After discovery, tag each tool in the Gleipnir UI (**Settings → MCP Servers → [server] → Tools**). Recommended tags:

**Todoist:**

| Tool | Tag |
|---|---|
| `todoist.get_tasks` | sensor |
| `todoist.get_task` | sensor |
| `todoist.get_projects` | sensor |
| `todoist.get_project` | sensor |
| `todoist.create_task` | actuator |
| `todoist.update_task` | actuator |
| `todoist.close_task` | actuator |
| `todoist.delete_task` | actuator |

**Mealie:** Tag after discovery — tool names depend on the server's implementation.

- Read-only tools (get/list/search recipes) → `sensor`
- Write tools (create/update/delete recipes) → `actuator`

**Note on tool names:** The policy YAML uses dot-separated names (e.g., `todoist.get_tasks`). In the agent's tool registry these become underscore-separated after sanitization (`todoist_get_tasks`), but in your policy YAML always use the dot form.

### Sample policies

**Policy 1 — Todoist sensor-only (webhook trigger)**

Reads open tasks from Todoist and sends a summary via the feedback channel. Uses only sensor capabilities.

```yaml
name: todoist-daily-summary
description: Fetch open Todoist tasks and send a summary via feedback.

trigger:
  type: webhook

capabilities:
  tools:
    - tool: todoist.get_projects
    - tool: todoist.get_tasks
  feedback:
    enabled: true

agent:
  task: |
    You have received a webhook trigger. Fetch all open tasks from Todoist
    across all projects. Group them by project. Send a concise summary of
    what is open via gleipnir.ask_operator so the operator can review it.
  limits:
    max_tokens_per_run: 20000
    max_tool_calls_per_run: 20
  concurrency: skip
```

**Policy 2 — Todoist with approval gate (webhook trigger)**

Creates a new task (no approval required) and then closes a task whose ID is provided in the webhook payload (approval required). Demonstrates the `waiting_for_approval` flow.

```yaml
name: todoist-create-and-close
description: Create a test task, then close a target task with approval.

trigger:
  type: webhook

capabilities:
  tools:
    - tool: todoist.get_task
    - tool: todoist.create_task
    - tool: todoist.close_task
      approval: required
      timeout: 10m
      on_timeout: reject

agent:
  task: |
    The webhook payload contains a field "task_id" — the ID of a Todoist task
    to close.

    1. Create a new Todoist task with the content "Test task created by Gleipnir".
    2. Fetch the task identified by "task_id" from the payload and confirm it exists.
    3. Close that task. This step requires operator approval — the run will pause
       until the operator approves or rejects in the Gleipnir UI.
  limits:
    max_tokens_per_run: 20000
    max_tool_calls_per_run: 20
  concurrency: skip
```

**Policy 3 — Mealie sensor-only (webhook trigger)**

Searches for recipes matching a query from the webhook payload and returns a formatted list via feedback.

```yaml
name: mealie-recipe-search
description: Search Mealie recipes matching a query from the webhook payload.

trigger:
  type: webhook

capabilities:
  tools:
    - tool: mealie.search_recipes
  feedback:
    enabled: true

agent:
  task: |
    The webhook payload contains a field "query" — a search term.
    Search for recipes in Mealie that match this query.
    Format the results as a numbered list with the recipe name and a brief
    description for each result. Send the formatted list via gleipnir.ask_operator.
  limits:
    max_tokens_per_run: 20000
    max_tool_calls_per_run: 10
  concurrency: skip
```

### Manual QA checklist

1. **Tool discovery** — after registering each server and clicking Discover, confirm the expected tools appear in the tool list. **Pass:** tool count > 0 for both servers; tool names include the server prefix (e.g., `todoist.get_tasks`, `mealie.search_recipes`).

2. **Sensor-only run (Todoist)** — fire the `todoist-daily-summary` policy via `POST /api/v1/webhooks/<policy_id>` with an empty JSON body (`{}`). **Pass:** run status is `complete`; the trace contains no actuator tool calls; a feedback message with a task summary is visible in the run detail.

3. **Actuator run without approval gate** — create a policy with only `todoist.create_task` (no `approval: required`). Fire the webhook. **Pass:** run status is `complete`; a new task appears in your Todoist inbox; the trace shows the `create_task` tool call and its result.

4. **Actuator run with approval gate** — fire the `todoist-create-and-close` policy with a payload like `{"task_id": "<an existing task id>"}`. **Pass:** run enters `waiting_for_approval` state and pauses. Approve the request in the Gleipnir UI. The run resumes and completes. The target task is closed in Todoist.

5. **Rejection path** — repeat the approval gate run but click **Reject** instead of Approve. **Pass:** run ends in `failed` state; the rejection reason is recorded in the trace; the target task is **not** closed in Todoist.

6. **Full reasoning trace** — for any completed run, open the run detail in the UI. **Pass:** the trace shows all step types — agent thinking steps, tool call inputs, tool call results, and (where applicable) approval events. All are visible in the correct order.

7. **Re-discovery** — tag a Todoist tool that was previously untagged, then click Discover again on the Todoist server. **Pass:** tool count and tag assignments are updated in the UI without needing to re-register the server.
