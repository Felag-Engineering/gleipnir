# Plan the week's meals

**Status:** Complete

## What it does

On a weekly cron trigger, this agent reads the next 7 days of your Google Calendar via `gcal.list_events` and identifies evenings that do not already have a dinner planned (any event whose title contains "dinner", "supper", or similar). For each empty slot it picks a recipe from your local [Mealie](https://mealie.io) instance via `mealie.get_recipes`, avoiding repeating a recipe within the same week. Once all slots are filled, it creates the week's meal plan in Mealie via `mealie.create_mealplan_bulk`. If the agent is unsure which calendar to read or encounters an unexpected dietary constraint, it pauses the run and asks you via the feedback channel before proceeding.

## Prerequisites

- A running Gleipnir instance (see main `README.md`).
- Docker and Docker Compose available on the same host (or a host reachable from Gleipnir).
- A Google account with access to Google Calendar.
- A Google Cloud project with the **Google Calendar API** enabled and an OAuth 2.0 Desktop application client configured (used by `gcal-mcp`).
- A running Mealie instance with at least a few recipes added.

## MCP servers used

| Server | Purpose | Source | Auth |
|--------|---------|--------|------|
| `gcal-mcp` | Read Google Calendar events | [nspady/google-calendar-mcp](https://github.com/nspady/google-calendar-mcp) | Google OAuth — Desktop app credentials JSON |
| `mealie-mcp` | Search recipes and create meal plans | [that0n3guy/mealie-mcp-server-ts](https://github.com/that0n3guy/mealie-mcp-server-ts) | Mealie API key |

## Step 1 — Get your credentials

### Google Calendar OAuth credentials

`gcal-mcp` uses OAuth 2.0 to read your calendar on your behalf. Follow the upstream [Google Cloud Setup guide](https://github.com/nspady/google-calendar-mcp#google-cloud-setup) to:

1. Create (or select) a Google Cloud project.
2. Enable the **Google Calendar API**.
3. Create an **OAuth 2.0 Client ID** of type **Desktop app**.
4. Download the JSON credentials file.
5. Save the file as `gcp-oauth.keys.json` in this directory (next to `docker-compose.yml`). The Compose file mounts this path read-only into the `gcal-mcp` container.

#### First-run OAuth token flow

Supergateway wraps the `gcal-mcp` process and has no mechanism to handle interactive prompts. Before bringing `gcal-mcp` up under Compose, run an ephemeral container directly to complete the one-time OAuth consent flow:

```bash
docker run --rm -it \
  -e GOOGLE_OAUTH_CREDENTIALS=/config/gcp-oauth.keys.json \
  -v ./gcp-oauth.keys.json:/config/gcp-oauth.keys.json:ro \
  -v gcal_tokens:/root/.config/google-calendar-mcp \
  node:22-alpine \
  npx @cocal/google-calendar-mcp auth
```

This opens an authorization URL in your terminal. Open it in a browser, grant access, and paste the authorization code back. The refresh token is stored in the `gcal_tokens` named Docker volume. The `gcal-mcp` Compose service mounts the same volume, so when you run `docker compose up -d` afterward the tokens are already in place and no further auth is needed.

> **Token expiry warning:** OAuth refresh tokens issued while your GCP app is in "test mode" expire after 7 days, which forces weekly re-authentication. To avoid this, publish the app to production mode via **OAuth consent screen → PUBLISH APP** in Google Cloud Console. The app will still show an "unverified app" warning to users but tokens will not expire on a 7-day cycle. See [upstream re-authentication docs](https://github.com/nspady/google-calendar-mcp#re-authentication). Alternatively, re-run the `npx @cocal/google-calendar-mcp auth` command each week.

### Mealie API key

`mealie-mcp` connects to your self-hosted Mealie instance using an API key.

1. Log in to your Mealie instance as an admin.
2. Go to **Profile → API Tokens → Generate**.
3. Give the token a name (e.g. `gleipnir`) and click **Generate**.
4. Copy the token — it is shown only once.

The two environment variables `mealie-mcp` expects are:

- `MEALIE_BASE_URL` — the base URL of your Mealie instance, e.g. `http://192.168.1.10:9000`. No trailing slash. Use the LAN IP rather than a hostname — container DNS will not resolve bare hostnames that aren't on the same Compose network.
- `MEALIE_API_KEY` — the API token generated above.

## Step 2 — Create .env

Create a file named `.env` inside `docs/playbooks/meal-planning/` — **the same directory as `docker-compose.yml`**. Docker Compose reads `.env` from the directory it is invoked in; placing it anywhere else will silently leave the variables unset.

```
MEALIE_BASE_URL=http://<mealie-host>:<port>
MEALIE_API_KEY=<paste Mealie API token here>
```

`gcp-oauth.keys.json` is a file mount, not an environment variable — it does not go in `.env`.

Do not commit `.env` or `gcp-oauth.keys.json` to version control. Both are listed in `.gitignore` at the repo root.

## Step 3 — Start the MCP servers

Run the following from this directory (the one containing `docker-compose.yml`):

```bash
cd docs/playbooks/meal-planning
docker compose up -d
```

Verify both services are running:

```bash
docker compose ps
```

Both should show `Up` status. If either shows `Exited`, check the logs:

```bash
docker compose logs gcal-mcp
docker compose logs mealie-mcp
```

## Step 4 — Register each MCP server in Gleipnir

In Gleipnir, go to **Settings → MCP Servers → Add Server** twice:

| Name | URL (same Compose project) | URL (separate host) |
|------|---------------------------|---------------------|
| `gcal` | `http://gcal-mcp:8101/` | `http://<MCP_HOST>:8101/` |
| `mealie` | `http://mealie-mcp:8102/` | `http://<MCP_HOST>:8102/` |

Use the **service name** as the hostname (`gcal-mcp`, `mealie-mcp`) when Gleipnir and the MCP servers are on the same Docker Compose network. Use the host IP and port numbers when they are on different hosts or Compose projects.

After adding each server, click **Discover**. Note the exact tool names returned — the policy YAML below references `gcal.list_events`, `mealie.get_recipes`, and `mealie.create_mealplan_bulk`. If Discover returns different names, update the `tool:` entries in the policy YAML to match before saving.

## Step 5 — Create the policy

Go to **Policies → New Policy** and fill in the form. The YAML below is the payload the form produces — it is included here as a reference and to make the field mapping explicit. The YAML tab was removed in the Gleipnir UI (ADR-019); use the form fields that correspond to each section.

Replace the substitution markers before saving:

- `<YOUR_CALENDAR_ID>` — the calendar to check for dinner events. Use `primary` for your default calendar, or find the calendar ID in Google Calendar settings (it looks like `example@group.calendar.google.com`).

```yaml
name: meal-planning
description: Check Google Calendar for unplanned dinners and fill them in Mealie's meal plan for the week.
folder: Household

model:
  provider: anthropic
  name: claude-sonnet-4-6
  options:
    enable_prompt_caching: true

trigger:
  type: cron
  cron_expr: "0 9 * * 0"   # 09:00 UTC every Sunday; adjust to match your timezone offset

capabilities:
  tools:
    - tool: gcal.list_events
    - tool: mealie.get_recipes
    - tool: mealie.create_mealplan_bulk
      approval: required
      timeout: 30m
      on_timeout: reject
  feedback:
    enabled: true
    timeout: 30m
    on_timeout: fail

agent:
  task: |
    Check <YOUR_CALENDAR_ID> via gcal.list_events for the next
    7 days starting tomorrow. For each evening, look for any
    event whose title suggests a dinner is already planned —
    titles containing words like "dinner", "supper", "meal",
    or a restaurant name. Evenings without such an event need
    a recipe assigned.

    For each evening that needs a recipe, call mealie.get_recipes
    to browse available recipes. Each result includes an "id"
    (UUID) and "name" — that is all you need. Do not attempt to
    fetch full recipe details. Avoid assigning the same recipe
    more than once in the week.

    Once you have a recipe for every unplanned evening, call
    mealie.create_mealplan_bulk with one entry per evening:
    { date, recipe_id, entry_type: "dinner" }. Pass the "id"
    field from get_recipes directly as recipe_id.

    If you are unsure which calendar to read, or uncertain about
    a dietary constraint, use the feedback channel to ask the
    operator before proceeding.
  limits:
    max_tokens_per_run: 30000
    max_tool_calls_per_run: 20
  concurrency: skip
```

**Why these choices:**

- `trigger.type: cron` with `cron_expr: "0 9 * * 0"` fires every Sunday at 09:00 UTC and runs indefinitely until the policy is paused. Adjust the time component to match your timezone offset (e.g. `0 17 * * 0` for 09:00 PT / UTC-8). The policy never auto-pauses — pause or delete it when you no longer want weekly runs.
- Only `mealie.create_mealplan_bulk` is approval-gated — it is the only write operation. Calendar reads and recipe lookups are read-only and do not require approval. The operator approves the meal plan before it is written, giving a final review of the week's choices.
- `feedback.enabled: true` gives the agent `gleipnir.ask_operator` (ADR-031) to handle dietary questions or calendar ambiguity without failing the run.
- `concurrency: skip` prevents a second run from starting if the Sunday trigger fires while a previous run is still waiting for approval. Without it, two runs could race to write conflicting meal plans to Mealie.
- Tools not listed in `capabilities.tools` are not registered with the agent at all — they literally do not exist from the agent's perspective (ADR-001).

## Step 6 — Trigger a test run

Before the first cron firing, test the policy by triggering it manually:

1. In Gleipnir, go to **Policies → meal-planning → Trigger** to start a run immediately.
2. Watch the run appear in the **Runs** list. Click into it to see the reasoning trace: tool calls, tool results, and the approval request for `mealie.create_mealplan_bulk`.
3. Review the proposed meal plan and approve. Verify the entries appear in Mealie under **Meal Plan**.

Once you have confirmed the agent produces a correct plan, the cron trigger will fire automatically each Sunday at the configured time. Pause the policy from the Policies list if you ever want to stop it.

## Extensions

### Avoid recently cooked meals

The base policy only avoids repeating a recipe within the current week, so the same dish could appear two weeks in a row. To extend the lookback window, add `mealie.get_all_mealplans` to `capabilities.tools` and extend the agent task:

```yaml
capabilities:
  tools:
    - tool: mealie.get_all_mealplans   # add this
    - tool: gcal.list_events
    - tool: mealie.get_recipes
    - tool: mealie.create_mealplan_bulk
      approval: required
      timeout: 30m
      on_timeout: reject
```

Then prepend to the agent task:

```
Before selecting recipes, call mealie.get_all_mealplans to retrieve
the meal plans for the past 3 weeks. Extract the recipe IDs already
used in that window and exclude them from your selections this week.
```

`get_all_mealplans` accepts `startDate` and `endDate` parameters — pass the date 21 days ago and today to limit the response to recent history.

### Calendar busyness-aware recipe selection

The `gcal.list_events` response already includes full event details for each evening — start time, end time, and how many events are on the calendar. The agent can use this to calibrate recipe complexity: suggest something quick on a packed evening and something more involved when the calendar is clear.

Extend the agent task with guidance on how to interpret busyness:

```
When selecting a recipe for each evening, also look at how many
calendar events that day has and how late they run:
- Evenings with events ending after 18:30, or 3+ events during
  the day: prefer recipes tagged "quick" or with totalTime under
  30 minutes if that information is available.
- Evenings with no events after 17:00: any recipe is suitable.
```

This requires no additional tools or capabilities — the calendar data is already in context from the `gcal.list_events` call made earlier in the run.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| Discover returns 0 tools for `gcal-mcp` | `gcp-oauth.keys.json` not mounted or missing | Confirm the file exists at `docs/playbooks/meal-planning/gcp-oauth.keys.json` and the Compose volume mount path matches. |
| Discover returns 0 tools for `mealie-mcp` | `MEALIE_BASE_URL` or `MEALIE_API_KEY` not set, or Mealie unreachable | Check that `.env` is in the same directory as `docker-compose.yml`. Verify Mealie is reachable from the Docker host: `curl $MEALIE_BASE_URL/api/app/about`. |
| `gcal-mcp` tools fail with authentication errors | Refresh token missing or expired | Re-run the ephemeral auth container: `docker run --rm -it ... npx @cocal/google-calendar-mcp auth` (full command in Step 1). |
| Google Calendar tokens expire after 7 days | GCP app is in "test mode" | Publish the OAuth consent screen to production mode, or re-run auth weekly. |
| `mealie.create_mealplan_bulk` fails | Recipe ID not found, or date format wrong | Check Discover output for the exact tool parameter schema. Verify the recipe IDs returned by `mealie.get_recipes` are being passed through correctly. |
| `.env` variables are not applied | `.env` is in the wrong directory | The file must be in `docs/playbooks/meal-planning/`, the same directory where you run `docker compose up`. |
| `docker compose down -v` deleted my Calendar tokens | `-v` flag removes all named volumes including `gcal_tokens` | Use `docker compose down` (without `-v`) to stop services without removing volumes. Re-run auth if tokens were deleted. |
| Tool names in policy don't match Discover output | MCP server updated its tool names | Click Discover again on each server in Settings → MCP Servers, then update the `tool:` entries in the policy to match the new names. |
