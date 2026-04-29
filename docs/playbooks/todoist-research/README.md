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

No API key is needed for web search — the playbook runs a self-hosted [SearXNG](https://docs.searxng.org/) meta-search engine and exposes it to the agent via an MCP wrapper. SearXNG queries Google, Bing, Brave, and ~70 other engines on your behalf, so you don't have to fight any single backend's bot detection (the previous DuckDuckGo MCP got rate-limited regularly).

## MCP servers used

| Server | Purpose | Source | Auth |
|--------|---------|--------|------|
| `todoist-mcp` | Read tasks, add comments, update labels | [stanislavlysenko0912/todoist-mcp-server](https://github.com/stanislavlysenko0912/todoist-mcp-server) | Todoist API token |
| `searxng` | Self-hosted meta-search backend (not directly registered in Gleipnir; only `searxng-mcp` talks to it) | [searxng/searxng](https://github.com/searxng/searxng) | None |
| `searxng-mcp` | Web search and URL fetch tools for the agent | [ihor-sokoliuk/mcp-searxng](https://github.com/ihor-sokoliuk/mcp-searxng) | None |

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

All three images are pulled from registries — no local build needed. Run the following from this directory (the one containing `docker-compose.yml`):

```bash
cd docs/playbooks/todoist-research
docker compose up -d
```

Verify all three services are running:

```bash
docker compose ps
```

All should show `Up` status. If any show `Exited`, check the logs:

```bash
docker compose logs todoist-mcp
docker compose logs searxng
docker compose logs searxng-mcp
```

You can also sanity-check SearXNG itself by hitting the host-exposed port directly:

```bash
curl 'http://localhost:8113/search?q=test&format=json' | head
```

A JSON response with a `results` array means SearXNG is working. If it returns HTML or an error about format support, the JSON format isn't enabled — confirm `searxng/settings.yml` lists `json` under `search.formats` and restart with `docker compose up -d --force-recreate searxng`.

## Step 4 — Register each MCP server in Gleipnir

In Gleipnir, go to **Tools → Add MCP server** twice (the `searxng` backend is internal — only `searxng-mcp` talks to it, so don't register it in Gleipnir):

| Name | URL (same Compose project) | URL (separate host) |
|------|---------------------------|---------------------|
| `todoist` | `http://todoist-mcp:8111/mcp` | `http://<MCP_HOST>:8111/mcp` |
| `searxng` | `http://searxng-mcp:8112/mcp` | `http://<MCP_HOST>:8112/mcp` |

Use the **service name** as the hostname (`todoist-mcp`, `searxng-mcp`) when Gleipnir and the MCP servers are on the same Docker Compose network. Use the host IP and port numbers when they are on different hosts or Compose projects. Note the `/mcp` path for `searxng-mcp` — `mcp-searxng` exposes its endpoint there, not at the root.

After adding each server, click **Discover**. Note the exact tool names returned — the policy YAML below references `todoist.get_tasks_list`, `todoist.create_comments`, `todoist.update_tasks`, and `searxng.searxng_web_search`. If Discover returns different names, update the `tool:` entries in the policy YAML to match before saving.

## Step 5 — Create the agent

Go to **Agents → New Agent** and fill in the form. The form is the only editing surface (ADR-019); each section below maps to one section in the editor.

**Identity**

- **Name:** `todoist-research`
- **Description:** `Poll Todoist for tasks labeled AI_Assist, research each one using web search, and post findings as a task comment.`
- **Folder:** `Productivity`

**Model**

- **Provider:** `anthropic`
- **Model:** `claude-sonnet-4-6`
- **Prompt caching:** enabled

**Trigger**

- **Type:** `poll`
- **Interval:** `15m`
- **Match mode:** `all`
- Add one **check**:
  - **Tool:** `todoist.get_tasks_list`
  - **Input:** paste the following JSON into the input textarea — the form takes JSON here, not YAML:
    ```json
    {"label": "AI_Assist"}
    ```
  - **JSONPath:** `$[0].id`
  - **Comparator:** `not equals`
  - **Value:** leave blank (empty string)

The check fires a run only when the array of tasks is non-empty (first element has a non-empty `id`). When no tasks match, the poll runs silently — no run is created and no UI signal is emitted (see Troubleshooting).

**Capabilities → Tools**

Add four tools. The first two are read-only; the last two are the only writes and stay approval-gated.

| Tool | Approval | Timeout | On timeout |
|------|----------|---------|------------|
| `todoist.get_tasks_list` | none | — | — |
| `searxng.searxng_web_search` | none | — | — |
| `todoist.create_comments` | required | `1h` | `reject` |
| `todoist.update_tasks` | required | `1h` | `reject` |

**Capabilities → Feedback**

- **Enabled:** on
- **Timeout:** `30m`
- **On timeout:** `fail`

This grants the agent `gleipnir.ask_operator` so it can ask for clarification on ambiguous tasks instead of guessing.

**Task instructions**

Paste the following into the task instructions field:

> Your trigger payload contains a JSON object with a `poll_results` array. Each entry includes a `result` field that is itself an array of Todoist tasks. Each task object includes an `id` field (the task ID), a `content` field (the task title), a `description` field (optional extra context), and a `labels` array (the task's current labels). Do not call `todoist.get_tasks_list` again — use the tasks already in your trigger payload.
>
> **You must complete every task in the trigger payload by making BOTH a `todoist.create_comments` tool call AND a `todoist.update_tasks` tool call. Do NOT write the research summary as a chat message — the only place the research text should appear is inside the `content` argument of `todoist.create_comments`. A response without those two tool calls is incomplete; keep going until both are made for every task.**
>
> For each task in the payload, follow this exact sequence of tool calls:
>
> 1. Call `searxng.searxng_web_search` to gather information about what the `content` and `description` fields ask for. For most tasks, 2–4 targeted searches are enough. Prefer searches that return specific, actionable results (business listings, official pages, how-to guides) over broad informational queries.
> 2. Call `todoist.create_comments` with an `items` array. Each item must contain **exactly two fields**:
>    - `task_id`: the task `id` from the trigger payload.
>    - `content`: a markdown-formatted research summary built from the search results, following these guidelines:
>      - Lead with a one-sentence summary of what you found.
>      - Present options or results as a numbered list.
>      - For each item include: name, relevant detail (address/price/description), website URL if available, and contact info if applicable.
>      - Keep the comment concise — a useful reference, not an essay. Aim for 200–500 words.
>      - If the task asks for local services, include distance or neighborhood context when the search results provide it.
>      - End with a line noting the research date so the user knows how fresh it is.
>
>    **OMIT the `project_id` field entirely** — do not include it at all, not even as an empty string, `null`, or a blank value. Including it in any form will cause Todoist to reject the request with `Invalid ProjectID` or route the comment to the project instead of the task. The same rule applies to any other optional field: pass ONLY `task_id` and `content`.
> 3. Call `todoist.update_tasks` with an `items` array to remove the `AI_Assist` label. Each item must contain **exactly three fields**:
>    - `task_id`: the task `id` from the trigger payload.
>    - `content`: the task's existing `content` value from the trigger payload, passed back **unchanged**. This is required by the wrapper schema even on update — omitting it returns `Invalid arguments for tool update_tasks: items.0.content Required`.
>    - `labels`: the task's existing `labels` array minus `AI_Assist` (use the `labels` from the trigger payload, not from your earlier search results). If the task has no other labels, pass `[]`.
>
>    **OMIT every other field** — no `description`, no `due_date`, no `priority`, no `assignee_id`, etc. Empty strings count as values and Todoist will reject them with `Invalid argument value`. Use `todoist.update_tasks` — NOT `todoist.update_labels`, which mutates a label entity by label ID and will fail with `label_id is invalid` if given a task ID.
>
> If you are uncertain what a task is asking for — for example, the title is ambiguous or missing key context like a location — call `gleipnir.ask_operator` before searching. Process every task in the trigger payload; the run is complete only after every task has had both `todoist.create_comments` and `todoist.update_tasks` called for it.

**Run limits**

- **Max tokens per run:** `40000`
- **Max tool calls per run:** `40`

**Concurrency**

- **Policy:** `queue`
- **Queue depth:** `3`

**Why these choices:**

- The 15-minute poll calls `todoist.get_tasks_list` filtered by `AI_Assist`, then evaluates `$[0].id not_equals ""` — fires only when at least one matching task exists, stays silent otherwise. Polls that find nothing consume one Todoist API call but no LLM tokens.
- The task instructions tell the agent to read from its trigger payload rather than re-fetching. The runtime delivers poll results as the first user message, so the task list is already in context — calling `get_tasks_list` again would double API calls for no benefit.
- `create_comments` and `update_tasks` are approval-gated because they are the only write operations. The 1-hour window gives you time to review before anything is posted to Todoist. If you prefer fully automatic posting, set both approvals to `none` (see Extensions).
- `concurrency: queue` (depth 3) lets tasks labeled while a run is in progress accumulate rather than drop. With 15-minute polling, deep queuing is unlikely.
- Tools you do not add to the **Capabilities → Tools** list are not registered with the agent at all — they literally do not exist from the agent's perspective.

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

If you trust the agent's output and find the approval step inconvenient, edit the agent and change the **Approval** dropdown on `todoist.create_comments` to **none**. The comment is then posted immediately, while the label removal on `todoist.update_tasks` still requires approval — so you only confirm one step per task instead of two. To make the whole pipeline fully automatic, set both tools' approvals to **none**.

### Add page fetching for deep dives

The `searxng-mcp` server also exposes a `web_url_read` tool that fetches a specific URL and returns its content as Markdown. This is useful for tasks that need more than search snippets — reading an official policy page, pulling contact details from a business website, or following up on a result.

Edit the agent, open **Capabilities → Tools**, click **Add tool**, pick `searxng.web_url_read`, and leave the approval dropdown at **none** (it is read-only). No additional infrastructure is needed — `web_url_read` is already part of the `searxng-mcp` container.

### Add a second label for high-priority tasks

If you want some tasks researched immediately rather than waiting for the next poll, add a webhook policy that fires when Todoist sends a task-created event via their webhook integration. The webhook-triggered policy can share the same MCP servers — register a second policy that targets `AI_Assist_Now` and uses a `webhook` trigger instead of `poll`.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| Poll seems silent — no runs appear, even with labeled tasks | Check condition is matching `false` (no tasks with the label) — non-matching evaluations are silent in the UI | The **Next:** time on the agent card is when the next *check* will run, not the next run start. Polls that find no tasks complete without producing a run or any UI signal. To confirm the poll is firing, set `GLEIPNIR_LOG_LEVEL=debug` and look for `poller: checks did not match` lines in the server log, or use **Run now** to trigger manually. |
| Poll fires but agent says no tasks found | `AI_Assist` label spelling mismatch | Todoist label names are case-sensitive. Check the exact label name on the task matches the value you entered in the trigger check input JSON. |
| Poll never triggers a run, even with labeled tasks | `get_tasks_list` returning unexpected JSON structure | On the **Tools** page, click Discover on the `todoist` server, then call `get_tasks_list` manually with `{"label": "AI_Assist"}`. Inspect the raw JSON to confirm `$[0].id` resolves to a task ID string. |
| `todoist-mcp` Discover returns 0 tools | `TODOIST_API_KEY` not set in `.env` | Check that `.env` is in the same directory as `docker-compose.yml`. The compose file maps `TODOIST_API_KEY` to the `API_KEY` env var the package expects — confirm both names match. |
| `searxng` Discover returns 0 tools | `searxng-mcp` cannot reach the SearXNG backend, or SearXNG is not returning JSON | `docker compose logs searxng-mcp` will show the connect error. Confirm `searxng/settings.yml` lists `json` under `search.formats`, then `docker compose up -d --force-recreate searxng searxng-mcp`. |
| `searxng_web_search` returns empty results for every query | One of the upstream search engines SearXNG queries is failing, or the host is being rate-limited by all of them at once | Look at `docker compose logs searxng` for `engine timeout` / `403` lines. SearXNG queries multiple engines in parallel — if a few fail, results still come through; if all fail, no results. Bot-detection at the residential IP level can affect every engine simultaneously. |
| `web_url_read` returns 403 / Cloudflare challenge for some URLs | The target site blocks server-side fetchers | This is fundamental — `web_url_read` runs a plain HTTP fetch. For sites behind Cloudflare or similar, expect failure. Fall back to using `searxng_web_search` snippets for those tasks. |
| Comment is posted but label not removed | `update_tasks` approval timed out (1h) | Approve the `update_tasks` request in Gleipnir before the timeout expires, or widen the timeout in the tool's settings. Alternatively change the approval on `update_tasks` to **none** to make label removal automatic. |
| Run hits token limit | Too many tasks in one poll batch | Add a `limit` field to the trigger check input JSON (e.g. `{"label": "AI_Assist", "limit": 3}`) to cap the number of tasks per run. |
| `.env` variables are not applied | `.env` is in the wrong directory | The file must be in `docs/playbooks/todoist-research/`, the same directory where you run `docker compose up`. |
| Tool names in agent don't match Discover output | MCP server updated its tool names | Click Discover again on each server on the **Tools** page, then edit the agent and update the tool entries to match the new names. |
