# Plan the week's meals

**Status:** Complete

## What it does

On a weekly cron trigger, this agent queries your Google Calendar (via the Arcade MCP gateway) for events over the next 7 days and identifies evenings that do not already have a dinner planned (any event whose title contains "dinner", "supper", "meal", or similar). For each empty slot it picks a recipe from your local [Mealie](https://mealie.io) instance via `mealie.get_recipes`, avoiding repeating a recipe within the same week. Once all slots are filled, it creates the week's meal plan in Mealie via `mealie.create_mealplan_bulk`. If the agent is unsure about a dietary constraint or encounters unexpected calendar data, it pauses the run and asks you via the feedback channel before proceeding.

## Prerequisites

- A running Gleipnir instance (see main `README.md`).
- Docker and Docker Compose available on the same host (or a host reachable from Gleipnir).
- A running Mealie instance with at least a few recipes added.
- An Arcade MCP gateway already registered in Gleipnir with the **GoogleCalendar** toolkit authorized for your user_id. Follow the [Arcade playbook](../arcade/README.md) first if you have not set this up. The meal-planning agent only needs `GoogleCalendar_ListEvents` from that toolkit.

## MCP servers used

| Server | Purpose | Source | Auth |
|--------|---------|--------|------|
| `arcade` | Read Google Calendar events | [arcade.dev](https://www.arcade.dev) — hosted MCP gateway | Arcade API key + user ID (configured per the [Arcade playbook](../arcade/README.md)) |
| `mealie-mcp` | Search recipes and create meal plans | [that0n3guy/mealie-mcp-server-ts](https://github.com/that0n3guy/mealie-mcp-server-ts) | Mealie API key |

The Arcade gateway is shared with any other playbook that uses it — register it once and reuse. Mealie is local to this playbook.

## Step 1 — Get your Mealie API key

1. Log into your Mealie instance
2. Click your profile icon → **Profile** → scroll to **API Tokens**
3. Click **Generate**, give it a name (e.g. `gleipnir`), click **Generate**
4. Copy the token — it is shown only once

## Step 2 — Create .env

Create a file named `.env` inside `docs/playbooks/meal-planning/` — **the same directory as `docker-compose.yml`**:

```
MEALIE_BASE_URL=http://<mealie-host>:<port>
MEALIE_API_KEY=<paste Mealie API token here>
```

Use the LAN IP and port of your Mealie instance (e.g. `http://192.168.1.10:9000`). No trailing slash.

Do not commit `.env` to version control. It is listed in `.gitignore` at the repo root.

## Step 3 — Start Mealie's MCP server

Run the following from the `docs/playbooks/meal-planning/` directory:

```bash
docker compose up -d
```

Verify the service is running:

```bash
docker compose ps
```

It should show `Up`. If it shows `Exited`, check the logs:

```bash
docker compose logs mealie-mcp
```

## Step 4 — Register Mealie in Gleipnir

The Arcade gateway is already registered if you completed the prerequisite. You only need to add Mealie.

In Gleipnir, go to **Tools → Add MCP server**:

| Name | URL (same Compose project) | URL (separate host) |
|------|---------------------------|---------------------|
| `mealie` | `http://mealie-mcp:8102/` | `http://<MCP_HOST>:8102/` |

Click **Discover** after saving. The policy references `mealie.get_recipes` and `mealie.create_mealplan_bulk`. If Discover returns different names, update the `tool:` entries in the policy to match before saving.

This playbook assumes the Arcade gateway is registered as **`arcade`** in Gleipnir. If you named it differently (e.g. `arcade-gateway`), substitute that name everywhere `arcade.` appears in the policy YAML below.

## Step 5 — Create the policy

Go to **Agents → New Agent** and fill in the form using the values below.

**Identity**
- Name: `meal-planning`
- Description: `Check Google Calendar for unplanned dinners and fill them in Mealie's meal plan for the week.`
- Folder: `Household`

**Trigger:** Cron — `0 9 * * 0` (09:00 UTC every Sunday — adjust for your timezone, e.g. `0 17 * * 0` for 09:00 PT / UTC-8)

**Capabilities:**
- Tools: `arcade.GoogleCalendar_ListEvents`, `mealie.get_recipes`, `mealie.create_mealplan_bulk` (approval required, timeout 30m, on timeout: reject)
- Feedback: enabled, timeout 30m, on timeout: fail

**Model:** claude-sonnet-4-6 (Anthropic) with prompt caching enabled

**Task:**
```
Call arcade.GoogleCalendar_ListEvents with min_end_datetime set to tomorrow
00:00 in the calendar's timezone and max_start_datetime set to 7 days after
that. The result is a JSON array of event objects with summary, start, and
end fields.

Walk the next 7 evenings (5pm–10pm local time on each day). For each
evening, look for any event whose summary contains words like "dinner",
"supper", "meal", or a restaurant name. Evenings without such an event
need a recipe assigned.

For each evening that needs a recipe, call mealie.get_recipes to browse
available recipes. Each result includes an "id" (UUID) and "name" — that is
all you need. Avoid assigning the same recipe more than once in the week.

Once you have a recipe for every unplanned evening, call
mealie.create_mealplan_bulk with one entry per evening:
{ date, recipe_id, entry_type: "dinner" }. Pass the "id" field from
get_recipes directly as recipe_id.

If you are unsure about a dietary constraint or encounter unexpected calendar
data, use the feedback channel to ask the operator before proceeding.
```

**Run limits:** max tokens 30000, max tool calls 20

**Concurrency:** Skip

**Why these choices:**

- `trigger.type: cron` with `cron_expr: "0 9 * * 0"` fires every Sunday at 09:00 UTC and runs indefinitely until the policy is paused.
- Calendar access goes through Arcade's pre-authorized OAuth grant — no per-policy credentials needed. The Arcade gateway holds the OAuth token; Gleipnir holds only the static API key for the gateway.
- Only `mealie.create_mealplan_bulk` is approval-gated — it is the only write operation. The calendar query and recipe lookups are read-only and do not require approval. The operator approves the meal plan before it is written.
- `feedback.enabled: true` gives the agent `gleipnir.ask_operator` to handle dietary questions or calendar ambiguity without failing the run.
- `concurrency: skip` prevents a second run from starting if the Sunday trigger fires while a previous run is still waiting for approval.

## Step 6 — Trigger a test run

Before the first cron firing, test the policy by triggering it manually:

1. In Gleipnir, go to **Agents → meal-planning → Run now**
2. Watch the run appear in **Run History**. Click into it to see the reasoning trace: the calendar query, recipe selection, and the approval request for `mealie.create_mealplan_bulk`.
3. Review the proposed meal plan and approve. Verify the entries appear in Mealie under **Meal Plan**.

Once confirmed, the cron trigger fires automatically each Sunday. Pause the policy from the Agents list if you want to stop it.

## Extensions

### Avoid recently cooked meals

Add `mealie.get_all_mealplans` to capabilities and prepend to the task:

```
Before selecting recipes, call mealie.get_all_mealplans to retrieve the meal
plans for the past 3 weeks (pass startDate 21 days ago and endDate today).
Extract the recipe IDs already used in that window and exclude them from your
selections this week.
```

### Calendar busyness-aware recipe selection

The Arcade response already includes full event details. Extend the task with:

```
When selecting a recipe for each evening, also check how busy that day looks
in the calendar response. If there are events ending after 18:30 or 3+ events
during the day, prefer recipes with "quick" in the name or short total time.
On clear evenings, any recipe is suitable.
```

No additional tools needed — the calendar data is already in context from the `arcade.GoogleCalendar_ListEvents` call.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| `arcade.GoogleCalendar_ListEvents` returns `RefreshError` or `auth_required` | The Arcade gateway has stale or missing OAuth credentials for your user_id | Re-authorize the GoogleCalendar toolkit per the [Arcade playbook](../arcade/README.md) Step 5. If the error persists with a freshly authorized user_id, the issue is upstream of Gleipnir. |
| Discover returns 0 tools for `arcade` | API key missing, wrong gateway URL, or no toolkits added in the Arcade dashboard | See the [Arcade playbook](../arcade/README.md) troubleshooting section. |
| Discover returns 0 tools for `mealie-mcp` | `MEALIE_BASE_URL` or `MEALIE_API_KEY` not set, or Mealie unreachable | Check `.env` is in the same directory as `docker-compose.yml`. Verify Mealie is reachable: `curl $MEALIE_BASE_URL/api/app/about`. |
| `mealie.create_mealplan_bulk` fails | Recipe ID not found or date format wrong | Check Discover output for the exact parameter schema. Verify recipe IDs from `mealie.get_recipes` are passed through correctly. |
| `.env` variables not applied | `.env` is in the wrong directory | The file must be in `docs/playbooks/meal-planning/`, the same directory where you run `docker compose up`. |
