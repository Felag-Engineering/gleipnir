# Plan the week's meals

**Status:** Complete

## What it does

On a weekly cron trigger, this agent reads your Google Calendar and identifies evenings over the next 7 days that do not already have a dinner planned (any event whose title contains "dinner", "supper", "meal", or similar). For each empty slot it picks a recipe from your local [Mealie](https://mealie.io) instance via `mealie.get_recipes`, avoiding repeating a recipe within the same week. Once all slots are filled, it creates the week's meal plan in Mealie via `mealie.create_mealplan_bulk`. If the agent is unsure about a dietary constraint or encounters unexpected calendar data, it pauses the run and asks you via the feedback channel before proceeding.

## Prerequisites

- A running Gleipnir instance (see main `README.md`).
- Docker and Docker Compose available on the same host (or a host reachable from Gleipnir).
- A running Mealie instance with at least a few recipes added.
- An Arcade account with Google Calendar connected, and an `arcade-google` MCP server registered in Gleipnir. If you have not set that up yet, follow [Connect Gleipnir to Arcade](../arcade/README.md) first — you need an Arcade API key, a gateway with the Google Calendar toolkit, and a successful `GoogleCalendar.WhoAmI` smoke test before continuing.

## MCP servers used

| Server | Purpose | Source | Auth |
|--------|---------|--------|------|
| `arcade-google` | Reads Google Calendar events for the upcoming week | [Arcade Google Calendar toolkit](https://docs.arcade.dev/toolkits/productivity/google-calendar) | `Authorization: Bearer <arcade-api-key>` + `Arcade-User-ID` headers |
| `mealie-mcp` | Search recipes and create meal plans | [that0n3guy/mealie-mcp-server-ts](https://github.com/that0n3guy/mealie-mcp-server-ts) | Mealie API key |

## Step 1 — Confirm the Arcade Google Calendar tools are reachable

You should have already completed the [Arcade onboarding playbook](../arcade/README.md) and verified `GoogleCalendar.WhoAmI` returns your email. If you haven't, stop here and do that first — the rest of this playbook assumes the Arcade path is working end-to-end.

In Gleipnir, open **Settings → MCP Servers → arcade-google → Discover** and confirm that `GoogleCalendar.ListEvents` appears in the tool list. If it does not, the Google Calendar toolkit is missing from your Arcade gateway — add it in the Arcade dashboard, then Discover again.

## Step 2 — Get your Mealie API key

1. Log into your Mealie instance.
2. Click your profile icon → **Profile** → scroll to **API Tokens**.
3. Click **Generate**, give it a name (e.g. `gleipnir`), click **Generate**.
4. Copy the token — it is shown only once.

## Step 3 — Create .env

Create a file named `.env` inside `docs/playbooks/meal-planning/` — **the same directory as `docker-compose.yml`**:

```
MEALIE_BASE_URL=http://<mealie-host>:<port>
MEALIE_API_KEY=<paste Mealie API token here>
```

Use the LAN IP and port of your Mealie instance (e.g. `http://192.168.1.10:9000`). No trailing slash.

Do not commit `.env` to version control. It is listed in `.gitignore` at the repo root.

## Step 4 — Start the Mealie MCP server

Run the following from the `docs/playbooks/meal-planning/` directory:

```bash
docker compose up -d
```

Verify it is running:

```bash
docker compose ps
```

It should show `Up`. If it shows `Exited`, check the logs:

```bash
docker compose logs mealie-mcp
```

## Step 5 — Register the Mealie server in Gleipnir

In Gleipnir, go to **Tools → Add MCP server**:

| Name | URL (same Compose project) | URL (separate host) |
|------|---------------------------|---------------------|
| `mealie` | `http://mealie-mcp:8102/` | `http://<MCP_HOST>:8102/` |

No auth header needed for the Mealie server — the Mealie API key is passed to the container through `.env`. Click **Discover** and confirm `get_recipes` and `create_mealplan_bulk` are in the list. If Discover returns different names, update the `tool:` entries in the policy to match before saving.

## Step 6 — Create the policy

Go to **Agents → New Agent** and fill in the form using the values below.

**Identity**
- Name: `meal-planning`
- Description: `Check Google Calendar for unplanned dinners and fill them in Mealie's meal plan for the week.`
- Folder: `Household`

**Trigger:** Cron — `0 9 * * 0` (09:00 UTC every Sunday — adjust for your timezone, e.g. `0 17 * * 0` for 09:00 PT / UTC-8)

**Capabilities:**
- Tools: `arcade-google.GoogleCalendar.ListEvents`, `mealie.get_recipes`, `mealie.create_mealplan_bulk` (approval required on `create_mealplan_bulk` only, timeout 30m, on timeout: reject)
- Feedback: enabled, timeout 30m, on timeout: fail

**Model:** claude-sonnet-4-6 (Anthropic) with prompt caching enabled

**Task:**
```
Call arcade-google.GoogleCalendar.ListEvents to read events from your
primary calendar covering the next 7 days starting tomorrow. For each
evening, look for any event whose summary contains words like "dinner",
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
- Arcade handles the Google OAuth token for Calendar access, so the policy itself carries no calendar-specific secret — only the Arcade API key and user ID live in Gleipnir (on the `arcade-google` server), encrypted at rest.
- Only `mealie.create_mealplan_bulk` is approval-gated — it is the only write operation. The calendar read and recipe lookups are read-only and do not require approval. The operator approves the meal plan before it is written.
- `feedback.enabled: true` gives the agent `gleipnir.ask_operator` to handle dietary questions or calendar ambiguity without failing the run.
- `concurrency: skip` prevents a second run from starting if the Sunday trigger fires while a previous run is still waiting for approval.

## Step 7 — Trigger a test run

Before the first cron firing, test the policy by triggering it manually:

1. In Gleipnir, go to **Agents → meal-planning → Run now**.
2. Watch the run appear in **Run History**. Click into it to see the reasoning trace: the calendar call, event filtering, recipe selection, and the approval request for `mealie.create_mealplan_bulk`.
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

`GoogleCalendar.ListEvents` returns full event details. Extend the task with:

```
When selecting a recipe for each evening, also check how busy that day looks
in the calendar data. If there are events ending after 18:30 or 3+ events
during the day, prefer recipes with "quick" in the name or short total time.
On clear evenings, any recipe is suitable.
```

No additional tools needed — the calendar data is already in context from the `ListEvents` call.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| `arcade-google.GoogleCalendar.ListEvents` returns an authorization URL | Google not connected (or connection expired) for this Arcade user | Log in to the Arcade dashboard → Auth → Google → reconnect, granting the Calendar scopes. Retry the run. |
| `arcade-google` Test Connection returns 401 | Arcade API key wrong, expired, or missing `Bearer ` prefix | Regenerate the key in the Arcade dashboard and update the `Authorization` header on the server in Gleipnir. |
| `GoogleCalendar.ListEvents` returns events from the wrong calendar | Default calendar not `primary` | The tool defaults to `primary`. If you want a different calendar, pass its calendar ID explicitly in the task instructions. |
| Discover returns 0 tools on `arcade-google` | Google Calendar toolkit not included in the Arcade gateway, or gateway uses "Arcade Auth" mode | In the Arcade dashboard, open the gateway and confirm it includes the Google Calendar toolkit and that the auth mode is **Arcade Headers**. |
| Discover returns 0 tools for `mealie-mcp` | `MEALIE_BASE_URL` or `MEALIE_API_KEY` not set, or Mealie unreachable | Check `.env` is in the same directory as `docker-compose.yml`. Verify Mealie is reachable: `curl $MEALIE_BASE_URL/api/app/about`. |
| `mealie.create_mealplan_bulk` fails | Recipe ID not found or date format wrong | Check Discover output for the exact parameter schema. Verify recipe IDs from `mealie.get_recipes` are passed through correctly. |
| `.env` variables not applied | `.env` is in the wrong directory | The file must be in `docs/playbooks/meal-planning/`, the same directory where you run `docker compose up`. |
| Tool names in policy don't match Discover output | Arcade or Mealie MCP updated tool names | Click Discover again on the affected server and update the `tool:` entries in the policy. |
