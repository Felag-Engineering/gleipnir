# Plan the week's meals

**Status:** Complete

## What it does

On a weekly scheduled trigger, this agent reads the next 7 days of your Google Calendar via `gcal.list_events` and identifies evenings that do not already have a `dinner:` event. For each empty slot it searches your local [Mealie](https://mealie.io) instance for a recipe via `mealie.get_recipes` and retrieves full ingredient details via `mealie.get_recipe_detailed`, avoiding repeating a recipe within the same week. It aggregates all ingredients into a single deduplicated grocery list and then creates one Google Keep note titled `Grocery list — week of <YYYY-MM-DD>` via `keep.create_note`. If the agent is unsure which calendar to read or encounters an unexpected dietary constraint, it pauses the run and asks you via the feedback channel before proceeding.

## Prerequisites

- A running Gleipnir instance (see main `README.md`).
- Docker and Docker Compose available on the same host (or a host reachable from Gleipnir).
- A Google account with access to Google Calendar and Google Keep.
- A Google Cloud project with the **Google Calendar API** enabled and an OAuth 2.0 Desktop application client configured (used by `gcal-mcp`).

## MCP servers used

| Server | Purpose | Source | Auth |
|--------|---------|--------|------|
| `gcal-mcp` | Read Google Calendar events | [nspady/google-calendar-mcp](https://github.com/nspady/google-calendar-mcp) | Google OAuth — Desktop app credentials JSON |
| `mealie-mcp` | Search and retrieve recipes from Mealie | [that0n3guy/mealie-mcp-server-ts](https://github.com/that0n3guy/mealie-mcp-server-ts) | Mealie API key |
| `keep-mcp` | Create Google Keep notes | [feuerdev/keep-mcp](https://github.com/feuerdev/keep-mcp) | Google master token (see below) |

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

### Google Keep master token

`keep-mcp` uses [gkeepapi](https://gkeepapi.readthedocs.io/en/latest/) under the hood. gkeepapi does not accept a regular Google password — it requires a **master token** (a long-lived OAuth2 token scoped to Google services).

> **Security warning:** The Google master token grants access to all Google services reachable via `gpsoauth`, not just Google Keep. Use a dedicated Google account for this integration rather than your primary account.

Obtain the master token by following the [gkeepapi master token procedure](https://gkeepapi.readthedocs.io/en/latest/#obtaining-a-master-token). If your account has 2FA enabled, you must generate an [app password](https://support.google.com/accounts/answer/185833) and use that during the token retrieval flow.

The two environment variables `keep-mcp` expects are:

- `GOOGLE_EMAIL` — your Google account email address.
- `GOOGLE_MASTER_TOKEN` — the master token obtained above.

### Mealie API key

`mealie-mcp` connects to your self-hosted Mealie instance using an API key.

1. Log in to your Mealie instance as an admin.
2. Go to **Profile → API Tokens → Generate**.
3. Give the token a name (e.g. `gleipnir`) and click **Generate**.
4. Copy the token — it is shown only once.

The two environment variables `mealie-mcp` expects are:

- `MEALIE_BASE_URL` — the base URL of your Mealie instance, e.g. `http://mealie:9000` or `http://192.168.1.10:9000`. No trailing slash.
- `MEALIE_API_KEY` — the API token generated above.

## Step 2 — Create .env

Create a file named `.env` inside `docs/playbooks/meal-planning/` — **the same directory as `docker-compose.yml`**. Docker Compose reads `.env` from the directory it is invoked in; placing it anywhere else will silently leave the variables unset.

```
GOOGLE_EMAIL=you@example.com
GOOGLE_MASTER_TOKEN=<paste master token here>
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

Verify all three services are running:

```bash
docker compose ps
```

All three should show `Up` status. If any show `Exited`, check the logs:

```bash
docker compose logs gcal-mcp
docker compose logs mealie-mcp
docker compose logs keep-mcp
```

> **Note:** `keep-mcp` installs Python dependencies at container start (`pip install keep-mcp`) and then downloads Supergateway via `npx`. The first start takes 30–60 seconds. Subsequent starts are faster.

## Step 4 — Register each MCP server in Gleipnir

In Gleipnir, go to **Settings → MCP Servers → Add Server** three times:

| Name | URL (same Compose project) | URL (separate host) |
|------|---------------------------|---------------------|
| `gcal` | `http://gcal-mcp:8101/` | `http://<GLEIPNIR_HOST>:8101/` |
| `mealie` | `http://mealie-mcp:8102/` | `http://<GLEIPNIR_HOST>:8102/` |
| `keep` | `http://keep-mcp:8103/` | `http://<GLEIPNIR_HOST>:8103/` |

Use the **service name** as the hostname (`gcal-mcp`, `mealie-mcp`, `keep-mcp`) when Gleipnir and the MCP servers are on the same Docker Compose network. Use the host IP or hostname and port numbers when they are on different hosts or Compose projects.

After adding each server, click **Discover**. Note the exact tool names returned — the policy YAML below references `gcal.list_events`, `mealie.get_recipes`, `mealie.get_recipe_detailed`, and `keep.create_note`. If Discover returns different names, update the `tool:` entries in the policy YAML to match before saving.

## Step 5 — Create the policy

Go to **Policies → New Policy** and fill in the form. The YAML below is the payload the form produces — it is included here as a reference and to make the field mapping explicit. The YAML tab was removed in the Gleipnir UI (ADR-019); use the form fields that correspond to each section.

Replace the substitution markers before saving:

- `<YOUR_CALENDAR_ID>` — the calendar to check for dinner events. Use `primary` for your default calendar, or find the calendar ID in Google Calendar settings (it looks like `example@group.calendar.google.com`).
- `<GROCERY_NOTE_TITLE_PREFIX>` — the prefix for the Keep note title. Default: `Grocery list — week of`.
- `<FIRE_AT_TIMESTAMP>` — an ISO-8601 UTC timestamp for the first run (e.g. `2026-04-27T16:00:00Z` for next Sunday at 09:00 PT). Add more timestamps to schedule multiple weeks in advance.

```yaml
name: meal-planning
description: Fill empty dinner slots in Google Calendar and drop a grocery list into Google Keep.
folder: Household

model:
  provider: anthropic
  name: claude-sonnet-4-6
  options:
    enable_prompt_caching: true

trigger:
  type: scheduled
  fire_at:
    - "<FIRE_AT_TIMESTAMP>"   # e.g. 2026-04-27T16:00:00Z — next Sunday 09:00 PT

capabilities:
  tools:
    - tool: gcal.list_events
    - tool: mealie.get_recipes
    - tool: mealie.get_recipe_detailed
    - tool: keep.create_note
      approval: required
      timeout: 30m
      on_timeout: reject
  feedback:
    enabled: true
    timeout: 30m
    on_timeout: fail

agent:
  task: |
    For each evening in the next 7 days starting tomorrow, check
    <YOUR_CALENDAR_ID> via gcal.list_events for an event whose
    title begins with "dinner:". For evenings without one, select
    a recipe from the Mealie library:
      1. Call mealie.get_recipes to list available recipes.
      2. Call mealie.get_recipe_detailed for the chosen recipe to
         retrieve its full ingredient list and quantities.
    Avoid repeating a recipe within the same week.

    When you have recipes for all empty slots, aggregate the
    ingredients across all recipes into a single grocery list
    (deduplicated, quantities summed where possible). Create a
    Google Keep note via keep.create_note titled
    "<GROCERY_NOTE_TITLE_PREFIX> <Monday of the week, YYYY-MM-DD>"
    with the grocery list as the body and the chosen recipes
    (with dates) as a header section.

    If you are unsure which calendar to read, or uncertain about
    a dietary constraint, use the feedback channel to ask the
    operator.
  limits:
    max_tokens_per_run: 40000
    max_tool_calls_per_run: 60
  concurrency: skip
```

**Why these choices:**

- `trigger.type: scheduled` with `fire_at` is a list of one-shot ISO-8601 UTC timestamps. Add a new timestamp each week (or each month in advance) to keep it running. There is no recurring cron; when all timestamps are consumed the policy is automatically paused.
- Only `keep.create_note` is approval-gated. Calendar reads and Mealie recipe lookups are read-only and do not require approval. The operator approves the Keep write before it executes, giving a final review of the grocery list.
- `feedback.enabled: true` gives the agent `gleipnir.ask_operator` (ADR-031) to handle dietary questions or calendar ambiguity without failing the run.
- Tools not listed in `capabilities.tools` are not registered with the agent at all — they literally do not exist from the agent's perspective (ADR-001).

## Step 6 — Trigger a test run

Because `trigger.type: scheduled` is one-shot, the easiest way to test without consuming a real scheduled slot is to clone the policy and set `trigger.type: manual` on the clone:

1. Duplicate the policy in Gleipnir (or create a second policy with the same body and `trigger.type: manual`).
2. Go to **Policies → [your test policy] → Trigger** to start a run immediately.
3. Watch the run appear in the **Runs** list. Click into it to see the reasoning trace: tool calls, tool results, and the approval request for `keep.create_note`.
4. Approve the Keep write when prompted. Verify that a note appears in Google Keep.

Once you have confirmed the agent produces a correct grocery list, update the scheduled policy's `fire_at` list with real timestamps for future weeks.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| Discover returns 0 tools for `gcal-mcp` | `gcp-oauth.keys.json` not mounted or missing | Confirm the file exists at `docs/playbooks/meal-planning/gcp-oauth.keys.json` and the Compose volume mount path matches. |
| Discover returns 0 tools for `mealie-mcp` | `MEALIE_BASE_URL` or `MEALIE_API_KEY` not set, or Mealie unreachable | Check that `.env` is in the same directory as `docker-compose.yml`. Verify Mealie is reachable from the Docker host: `curl $MEALIE_BASE_URL/api/app/about`. |
| Discover returns 0 tools for `keep-mcp` | `GOOGLE_EMAIL` or `GOOGLE_MASTER_TOKEN` not set | Check that `.env` is in the same directory as `docker-compose.yml` and both variables are present. |
| `gcal-mcp` tools fail with `-32600` or authentication errors | Refresh token missing or expired | Re-run the ephemeral auth container: `docker run --rm -it ... npx @cocal/google-calendar-mcp auth` (full command in Step 1). |
| Google Calendar tokens expire after 7 days | GCP app is in "test mode" | Publish the OAuth consent screen to production mode, or re-run auth weekly. |
| Keep master token rejected | 2FA on the Google account | Generate an app password and use it instead of your regular password when obtaining the master token. |
| `.env` variables are not applied | `.env` is in the wrong directory | The file must be in `docs/playbooks/meal-planning/`, the same directory where you run `docker compose up`. |
| `docker compose down -v` deleted my Calendar tokens | `-v` flag removes all named volumes including `gcal_tokens` | Use `docker compose down` (without `-v`) to stop services without removing volumes. Re-run auth if tokens were deleted. |
| Tool names in policy don't match Discover output | MCP server updated its tool names | Click Discover again on each server in Settings → MCP Servers, then update the `tool:` entries in the policy to match the new names. |
| `keep-mcp` container is slow to start | First-run pip install and npx download | Wait 60 seconds and check `docker compose ps` again. Subsequent starts are faster. |
