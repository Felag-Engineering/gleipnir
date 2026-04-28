# Research your own todo list

**Status:** Complete

## What it does

On a 15-minute poll trigger, this agent queries your Todoist inbox for any tasks labeled `AI_Assist`. For each matching task it reads the task title and description to understand what to research, then uses web search to gather relevant information, and finally posts the findings as a comment on the original Todoist task. Once a task is processed, the agent removes the `AI_Assist` label so it is not researched again on the next poll.

**Example tasks the agent handles well:**

- "Find a physical therapist near Burlington VT" → 5 options with name, address, phone, and website
- "Best standing desks under $500" → ranked comparison with links
- "Dog-friendly hiking trails within 1 hour of Portland OR" → trail list with distance and difficulty
- "Plumbers in Travis County TX with good reviews" → list with contact info and review summary
- "How do I file for a homestead exemption in Maricopa County AZ" → step-by-step with official links

The agent is intentionally narrow — it only reads tasks labeled `AI_Assist`, posts structured research comments, and removes the label. It does not re-order, close, or modify tasks beyond removing the label and adding the comment.

## Prerequisites

- A running Gleipnir instance (see main `README.md`).
- Docker and Docker Compose available on the same host (or a host reachable from Gleipnir).
- A Todoist account (free tier is sufficient).

No API key is needed for web search — the DuckDuckGo MCP server uses DuckDuckGo's search endpoint without authentication.

## MCP servers used

| Server | Purpose | Source | Auth |
|--------|---------|--------|------|
| `todoist-mcp` | Read tasks, add comments, update labels | [stanislavlysenko0912/todoist-mcp-server](https://github.com/stanislavlysenko0912/todoist-mcp-server) | Todoist API token |
| `duckduckgo-mcp` | Web research | [nickclyde/duckduckgo-mcp-server](https://github.com/nickclyde/duckduckgo-mcp-server) | None |

## Step 1 — Get your Todoist API token

The Todoist MCP server authenticates with a personal API token (not OAuth — this is simpler and appropriate for single-user homelab use).

1. Log in to [todoist.com](https://todoist.com).
2. Go to **Settings → Integrations → Developer → API token**.
3. Copy the token — it is always visible in this view.

The `AI_Assist` label does not need to be created in advance. Todoist creates labels automatically the first time you apply one to a task.

## Step 2 — Create .env

Create a file named `.env` inside `docs/playbooks/todoist-research/` — **the same directory as `docker-compose.yml`**. Docker Compose reads `.env` from the directory it is invoked in; placing it anywhere else will silently leave the variables unset.

```
TODOIST_API_KEY=<paste Todoist API token here>
```

Do not commit `.env` to version control. It is listed in `.gitignore` at the repo root.

## Step 3 — Start the MCP servers

The DuckDuckGo container requires a one-time image build because the upstream package is Python-only. Run the following from this directory (the one containing `docker-compose.yml`):

```bash
cd docs/playbooks/todoist-research
docker compose build   # builds the duckduckgo-mcp image; only needed once
docker compose up -d
```

Verify both services are running:

```bash
docker compose ps
```

Both should show `Up` status. If either shows `Exited`, check the logs:

```bash
docker compose logs todoist-mcp
docker compose logs duckduckgo-mcp
```

## Step 4 — Register each MCP server in Gleipnir

In Gleipnir, go to **Tools → Add MCP server** twice:

| Name | URL (same Compose project) | URL (separate host) |
|------|---------------------------|---------------------|
| `todoist` | `http://todoist-mcp:8111/mcp` | `http://<MCP_HOST>:8111/mcp` |
| `duckduckgo` | `http://duckduckgo-mcp:8112/mcp` | `http://<MCP_HOST>:8112/mcp` |

Use the **service name** as the hostname (`todoist-mcp`, `duckduckgo-mcp`) when Gleipnir and the MCP servers are on the same Docker Compose network. Use the host IP and port numbers when they are on different hosts or Compose projects.

After adding each server, click **Discover**. Note the exact tool names returned — the policy YAML below references `todoist.get_tasks_list`, `todoist.create_comments`, `todoist.update_tasks`, and `duckduckgo.search`. If Discover returns different names, update the `tool:` entries in the policy YAML to match before saving.

## Step 5 — Create the policy

Go to **Agents → New Agent** and fill in the form. The YAML below is the payload the form produces — it is included here as a reference and to make the field mapping explicit.

```yaml
name: todoist-research
description: Poll Todoist for tasks labeled AI_Assist, research each one using web search, and post findings as a task comment.
folder: Productivity

model:
  provider: anthropic
  name: claude-sonnet-4-6
  options:
    enable_prompt_caching: true

trigger:
  type: poll
  interval: 15m
  checks:
    - tool: todoist.get_tasks_list
      input:
        label: AI_Assist
      path: "$[0].id"
      not_equals: ""

capabilities:
  tools:
    - tool: todoist.get_tasks_list
    - tool: duckduckgo.search
    - tool: todoist.create_comments
      approval: required
      timeout: 1h
      on_timeout: reject
    - tool: todoist.update_tasks
      approval: required
      timeout: 1h
      on_timeout: reject
  feedback:
    enabled: true
    timeout: 30m
    on_timeout: fail

agent:
  task: |
    Your trigger payload contains a JSON array of Todoist tasks that
    have the AI_Assist label applied. Each task object includes an
    "id" field (the task ID), a "content" field (the task title), and
    a "description" field (optional extra context). Do not call
    todoist.get_tasks_list again — use the tasks already in your
    trigger payload.

    For each task in the payload:

    1. Read the "content" and "description" fields carefully to
       understand what research is needed.

    2. Use duckduckgo.search to gather information. For most tasks,
       2–4 targeted searches are enough. Prefer searches that return
       specific, actionable results (business listings, official pages,
       how-to guides) over broad informational queries.

    3. Synthesize the search results into a clear, structured comment.
       Format guidelines:
       - Lead with a one-sentence summary of what you found.
       - Present options or results as a numbered list.
       - For each item include: name, relevant detail (address/price/
         description), website URL if available, and contact info if
         applicable.
       - Keep the comment concise — the goal is a useful reference,
         not an essay. Aim for 200–500 words.
       - If the task asks for local services, include distance or
         neighborhood context when the search results provide it.
       - Note the date the research was done at the bottom of the
         comment so the user knows how fresh it is.

    4. Call todoist.create_comments to post the formatted research to
       the task. Pass the task "id" from the trigger payload as the
       task_id parameter.

    5. Call todoist.update_tasks to remove the AI_Assist label from
       the task, so it is not re-processed on the next poll. Pass the
       task "id" and a "labels" list that contains the task's existing
       labels minus "AI_Assist". If the task has no other labels, pass
       an empty list.

    Process all tasks in the trigger payload before finishing.
    If you are uncertain what a task is asking for — for example, the
    title is ambiguous or missing key context like a location — use the
    feedback channel to ask the operator before searching.

  limits:
    max_tokens_per_run: 40000
    max_tool_calls_per_run: 40
  concurrency: queue
  queue_depth: 3
```

**Why these choices:**

- `trigger.type: poll` with `interval: 15m` checks Todoist every 15 minutes. The poll check calls `todoist.get_tasks_list` filtered by the `AI_Assist` label, then evaluates `$[0].id not_equals ""` — this fires a run only when at least one matching task exists (non-empty array → first task has a non-empty ID), and stays silent when there are none. Polls that find no matching tasks consume one Todoist API call but do not start a run or burn LLM tokens.
- The agent task tells the agent to read tasks from its trigger payload rather than re-fetching them. Gleipnir delivers the poll tool result as the first user message, so the task list is already in context — calling `get_tasks_list` again would double the API calls for no benefit.
- Both `todoist.create_comments` and `todoist.update_tasks` are approval-gated. These are the only write operations — reading tasks and searching the web are read-only. The 1-hour approval window (longer than the meal-planning playbook's 30 minutes) gives you time to review the proposed comment and label removal before they are posted to Todoist. If you prefer fully automatic posting, set `approval: none` on both tools.
- `feedback.enabled: true` gives the agent `gleipnir.ask_operator` to handle ambiguous tasks without failing the run.
- `concurrency: queue` (depth 3) allows tasks labeled while a run is in progress to accumulate rather than being dropped. If more than 3 triggers queue up the extras are dropped with a warning in the Gleipnir logs; in practice 15-minute polling makes deep queuing unlikely.
- Tools not listed in `capabilities.tools` are not registered with the agent at all — they literally do not exist from the agent's perspective.

## Step 6 — Label a task and test

Before waiting for the poll to fire, trigger a test run manually:

1. In Todoist, create a task with a clear research prompt, e.g.:
   `Find a physical therapist near Burlington VT`
2. Apply the `AI_Assist` label to the task.
3. In Gleipnir, go to **Agents → todoist-research → Run now** to start a run immediately without waiting for the 15-minute poll.
4. Watch the run appear in the **Runs** list. Click into it to see the reasoning trace: the search queries, and the approval requests for `create_comments` and `update_tasks`.
5. Review the proposed comment and label removal, then approve both. Verify the comment appears on the task in Todoist and that the `AI_Assist` label has been removed.

Once you confirm the agent produces useful output, the poll trigger will fire automatically every 15 minutes. Apply `AI_Assist` to any task to queue it for research.

## Extensions

### Skip approval for write operations

If you trust the agent's output and find the approval step inconvenient, you can drop the gate on `create_comments` while keeping it on `update_tasks` (the label removal). This means you review the label removal before it happens but the comment is posted immediately:

```yaml
capabilities:
  tools:
    - tool: todoist.get_tasks_list
    - tool: duckduckgo.search
    - tool: todoist.create_comments    # no approval gate
    - tool: todoist.update_tasks
      approval: required
      timeout: 1h
      on_timeout: reject
```

Or remove all approval gates to make the whole pipeline fully automatic.

### Add page fetching for deep dives

The DuckDuckGo MCP server also exposes a `fetch_content` tool that reads the full text of a specific URL. This is useful for tasks that need more than search snippets — reading an official policy page, pulling contact details from a business website, or following up on a result. Add it alongside `search`:

```yaml
capabilities:
  tools:
    - tool: todoist.get_tasks_list
    - tool: duckduckgo.search
    - tool: duckduckgo.fetch_content   # add: reads a specific URL
    - tool: todoist.create_comments
      approval: required
      timeout: 1h
      on_timeout: reject
    - tool: todoist.update_tasks
      approval: required
      timeout: 1h
      on_timeout: reject
```

No additional infrastructure is needed — `fetch_content` is already part of the `duckduckgo-mcp` container.

### Add a second label for high-priority tasks

If you want some tasks researched immediately rather than waiting for the next poll, add a webhook policy that fires when Todoist sends a task-created event via their webhook integration. The webhook-triggered policy can share the same MCP servers — register a second policy that targets `AI_Assist_Now` and uses a `webhook` trigger instead of `poll`.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| Poll fires but agent says no tasks found | `AI_Assist` label spelling mismatch | Todoist label names are case-sensitive. Check the exact label name on the task matches the `input.label` value in the policy trigger check. |
| Poll never triggers a run, even with labeled tasks | `get_tasks_list` returning unexpected JSON structure | Click Discover on the `todoist` server in Gleipnir and call `get_tasks_list` manually with `label: AI_Assist`. Inspect the raw JSON to confirm `$[0].id` resolves to a task ID string. |
| `todoist-mcp` Discover returns 0 tools | `TODOIST_API_KEY` not set in `.env` | Check that `.env` is in the same directory as `docker-compose.yml`. The compose file maps `TODOIST_API_KEY` to the `API_KEY` env var the package expects — confirm both names match. |
| `duckduckgo-mcp` Discover returns 0 tools | Image not built or build failed | Run `docker compose build duckduckgo-mcp` and check for errors. Confirm `docker compose ps` shows the container as `Up`. |
| DuckDuckGo returns no results for some queries | Rate limiting from DuckDuckGo | DuckDuckGo's endpoint may throttle bursts. Lower `max_tool_calls_per_run` to space out searches, or make each search broader to reduce the number of calls per task. |
| Comment is posted but label not removed | `update_tasks` approval timed out (1h) | Approve the `update_tasks` request in Gleipnir before the timeout expires, or widen the timeout. Alternatively remove the approval gate on `update_tasks` to make label removal automatic. |
| Run hits token limit | Too many tasks in one poll batch | Add a `limit` parameter to `todoist.get_tasks_list` in both the trigger check and the policy capabilities to cap the number of tasks per run (e.g. `limit: 3`). |
| `.env` variables are not applied | `.env` is in the wrong directory | The file must be in `docs/playbooks/todoist-research/`, the same directory where you run `docker compose up`. |
| Tool names in policy don't match Discover output | MCP server updated its tool names | Click Discover again on each server on the **Tools** page, then update the `tool:` entries in the policy to match the new names. |
