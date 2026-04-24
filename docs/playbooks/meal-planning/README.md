# Plan the week's meals

**Status:** Complete

## What it does

On a weekly cron trigger, this agent fetches your Google Calendar ICS feed and identifies evenings over the next 7 days that do not already have a dinner planned (any event whose title contains "dinner", "supper", "meal", or similar). For each empty slot it picks a recipe from your local [Mealie](https://mealie.io) instance via `mealie.get_recipes`, avoiding repeating a recipe within the same week. Once all slots are filled, it creates the week's meal plan in Mealie via `mealie.create_mealplan_bulk`. If the agent is unsure about a dietary constraint or encounters unexpected calendar data, it pauses the run and asks you via the feedback channel before proceeding.

## Prerequisites

- A running Gleipnir instance (see main `README.md`).
- Docker and Docker Compose available on the same host (or a host reachable from Gleipnir).
- A running Mealie instance with at least a few recipes added.
- Your Google Calendar private ICS URL (see Step 1).

## MCP servers used

| Server | Purpose | Source | Auth |
|--------|---------|--------|------|
| `fetch-mcp` | Fetches the Google Calendar ICS feed over HTTPS | [@modelcontextprotocol/server-fetch](https://github.com/modelcontextprotocol/servers/tree/main/src/fetch) | None — the secret is embedded in the ICS URL |
| `mealie-mcp` | Search recipes and create meal plans | [that0n3guy/mealie-mcp-server-ts](https://github.com/that0n3guy/mealie-mcp-server-ts) | Mealie API key |

## Step 1 — Get your Google Calendar ICS URL

No Google Cloud project or OAuth setup required. Google Calendar generates a private secret URL for each calendar that acts as its own credential.

1. Open [Google Calendar](https://calendar.google.com)
2. Click the gear icon → **Settings**
3. In the left sidebar, click the calendar you want the agent to read
4. Scroll down to **Integrate calendar**
5. Copy the **Secret address in iCal format** — it looks like:
   `https://calendar.google.com/calendar/ical/<id>/private-<secret>/basic.ics`

Keep this URL private — anyone with it can read your calendar. Do not commit it to version control. You will paste it directly into the Gleipnir policy task instructions in Step 5.

## Step 2 — Get your Mealie API key

1. Log into your Mealie instance
2. Click your profile icon → **Profile** → scroll to **API Tokens**
3. Click **Generate**, give it a name (e.g. `gleipnir`), click **Generate**
4. Copy the token — it is shown only once

## Step 3 — Create .env

Create a file named `.env` inside `docs/playbooks/meal-planning/` — **the same directory as `docker-compose.yml`**:

```
MEALIE_BASE_URL=http://<mealie-host>:<port>
MEALIE_API_KEY=<paste Mealie API token here>
```

Use the LAN IP and port of your Mealie instance (e.g. `http://192.168.1.10:9000`). No trailing slash.

Do not commit `.env` to version control. It is listed in `.gitignore` at the repo root.

## Step 4 — Start the MCP servers

Run the following from the `docs/playbooks/meal-planning/` directory:

```bash
docker compose up -d
```

Verify both services are running:

```bash
docker compose ps
```

Both should show `Up`. If either shows `Exited`, check the logs:

```bash
docker compose logs fetch-mcp
docker compose logs mealie-mcp
```

## Step 5 — Register each MCP server in Gleipnir

In Gleipnir, go to **Tools → Add MCP server** twice:

| Name | URL (same Compose project) | URL (separate host) |
|------|---------------------------|---------------------|
| `fetch` | `http://fetch-mcp:8101/` | `http://<MCP_HOST>:8101/` |
| `mealie` | `http://mealie-mcp:8102/` | `http://<MCP_HOST>:8102/` |

After adding each server, click **Discover**. Note the exact tool names returned — the policy references `fetch.fetch`, `mealie.get_recipes`, and `mealie.create_mealplan_bulk`. If Discover returns different names, update the `tool:` entries in the policy to match before saving.

## Step 6 — Create the policy

Go to **Agents → New Agent** and fill in the form using the values below.

Replace `<YOUR_ICS_URL>` with the secret iCal address from Step 1 before saving.

**Identity**
- Name: `meal-planning`
- Description: `Check Google Calendar for unplanned dinners and fill them in Mealie's meal plan for the week.`
- Folder: `Household`

**Trigger:** Cron — `0 9 * * 0` (09:00 UTC every Sunday — adjust for your timezone, e.g. `0 17 * * 0` for 09:00 PT / UTC-8)

**Capabilities:**
- Tools: `fetch.fetch`, `mealie.get_recipes`, `mealie.create_mealplan_bulk` (approval required, timeout 30m, on timeout: reject)
- Feedback: enabled, timeout 30m, on timeout: fail

**Model:** claude-sonnet-4-6 (Anthropic) with prompt caching enabled

**Task:**
```
Fetch the Google Calendar ICS feed at <YOUR_ICS_URL> using the fetch tool.
Parse the iCal data to identify the next 7 days starting tomorrow. For each
evening, look for any VEVENT whose SUMMARY contains words like "dinner",
"supper", "meal", or a restaurant name. Evenings without such an event need
a recipe assigned.

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
- The ICS URL contains a private secret that authenticates the request — no OAuth or API key needed for calendar access.
- Only `mealie.create_mealplan_bulk` is approval-gated — it is the only write operation. The calendar fetch and recipe lookups are read-only and do not require approval. The operator approves the meal plan before it is written.
- `feedback.enabled: true` gives the agent `gleipnir.ask_operator` to handle dietary questions or calendar ambiguity without failing the run.
- `concurrency: skip` prevents a second run from starting if the Sunday trigger fires while a previous run is still waiting for approval.

## Step 7 — Trigger a test run

Before the first cron firing, test the policy by triggering it manually:

1. In Gleipnir, go to **Agents → meal-planning → Run now**
2. Watch the run appear in **Run History**. Click into it to see the reasoning trace: the fetch call, iCal parsing, recipe selection, and the approval request for `mealie.create_mealplan_bulk`.
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

The iCal data already includes full event details. Extend the task with:

```
When selecting a recipe for each evening, also check how busy that day looks
in the iCal data. If there are events ending after 18:30 or 3+ events during
the day, prefer recipes with "quick" in the name or short total time. On clear
evenings, any recipe is suitable.
```

No additional tools needed — the calendar data is already in context from the fetch call.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| `fetch.fetch` returns 404 or empty | ICS URL is wrong or expired | Re-copy the Secret address in iCal format from Google Calendar settings. |
| `fetch.fetch` returns an HTML login page | Wrong URL — you copied the public address instead of the secret iCal address | The correct URL contains `/private-` and ends in `/basic.ics`. |
| Discover returns 0 tools for `mealie-mcp` | `MEALIE_BASE_URL` or `MEALIE_API_KEY` not set, or Mealie unreachable | Check `.env` is in the same directory as `docker-compose.yml`. Verify Mealie is reachable: `curl $MEALIE_BASE_URL/api/app/about`. |
| `mealie.create_mealplan_bulk` fails | Recipe ID not found or date format wrong | Check Discover output for the exact parameter schema. Verify recipe IDs from `mealie.get_recipes` are passed through correctly. |
| `.env` variables not applied | `.env` is in the wrong directory | The file must be in `docs/playbooks/meal-planning/`, the same directory where you run `docker compose up`. |
