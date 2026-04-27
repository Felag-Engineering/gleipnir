# Connect Gleipnir to Arcade

**Status:** Complete

## What Arcade is

[Arcade](https://www.arcade.dev) is a hosted MCP runtime that brokers OAuth for many SaaS tools — Gmail, Google Calendar, Slack, GitHub, and many others. Instead of managing OAuth flows yourself, you point Gleipnir at Arcade's MCP gateway and use your Arcade API key to authenticate. Arcade holds the downstream OAuth tokens server-side; Gleipnir never stores them.

Authorization is granted at the **OAuth-scope** level, not the toolkit level. Toolkits whose tools share a single scope (e.g. Google Calendar) finish in one click. Toolkits whose tools span multiple scopes (notably Gmail) prompt **once per distinct scope** — Gmail in particular typically requires 4–5 consecutive OAuth screens covering `gmail.send`, `gmail.readonly`, `gmail.modify`, `gmail.compose`, and `gmail.labels`. Plan to keep the browser focused for the full chain.

### Trust expansion warning

When you connect Gleipnir to Arcade, **downstream OAuth tokens for services like Gmail and Slack live with Arcade, not with Gleipnir.** Any policy whose capabilities include an Arcade-provided tool can instruct the agent to read your email, send Slack messages, or take other account-level actions on the connected service — within the scope of what that OAuth token permits.

This is an intentional trust expansion. Review the tools you grant to each policy carefully. Only grant the specific tools a policy actually needs, and consider using `approval: required` on write operations (e.g. sending email or posting to Slack) so you can review every action before it executes.

## Prerequisites

- A running Gleipnir instance with an encryption key configured.
- An Arcade account. Sign up at [arcade.dev](https://www.arcade.dev).
- A gateway created in the Arcade dashboard with at least one toolkit added.

## Step 1 — Create a gateway in Arcade and add toolkits

1. Log in to [arcade.dev](https://www.arcade.dev).
2. Navigate to **Gateways** and click **Create Gateway**.
3. Give it a name and note the gateway slug (e.g. `my-gateway`).
4. Under the gateway, click **Add Toolkit** and add the tool categories you need (e.g. Gmail, Google Calendar, Slack).

## Step 2 — Get your API key and user ID

1. In the Arcade dashboard, go to **Settings → API Keys**.
2. Copy your API key — it starts with `sk_live_` or `arcade_`.
3. Your user ID is the email address you used to sign up for Arcade.

## Step 3 — Register the MCP server in Gleipnir

1. In Gleipnir, go to **Tools → Add MCP server**.
2. Fill in the fields:
   - **Name:** `arcade-gateway` (or a descriptive name for this gateway)
   - **URL:** `https://api.arcade.dev/mcp/<gateway-slug>` (replace `<gateway-slug>` with your gateway's slug)
3. Under **Auth Headers**, click **Auth headers** to open the editor, then add two headers:
   - **Name:** `Authorization` — **Value:** `Bearer <your-api-key>`
   - **Name:** `Arcade-User-ID` — **Value:** `<your-email>`
4. Click **Test Connection** to verify Gleipnir can reach the gateway.
5. Click **Save**.

The API key and user ID are stored encrypted using AES-256-GCM. Neither value is ever returned in API responses — only the header names are visible after saving.

## Step 4 — Discover tools

After saving, Gleipnir attempts auto-discovery immediately. If it does not find tools, open the server detail panel and click **Rediscover**. The tool list should show toolkit-qualified names like `Gmail_SendEmail`, `GoogleCalendar_CreateEvent`, etc. (note the underscore — MCP tool names use `Toolkit_Action`). In policy YAML, you reference these as `<server-name>.<tool-name>`, e.g. `arcade-gateway.Gmail_SendEmail`.

## Step 5 — Authorize each toolkit

Before an agent can call Arcade tools, you must pre-authorize each toolkit for the user ID configured in step 3. This is a one-time OAuth flow per toolkit.

1. Open the server detail panel for your Arcade gateway.
2. In the **Toolkit authorization** section, you will see one row per toolkit (e.g. Gmail, GoogleCalendar, Slack).
3. Click **Check →** next to a toolkit. Gleipnir calls Arcade's pre-auth API:
   - If the toolkit is already authorized (from a previous session or a different machine), the row flips to **✓ Authorized**.
   - If authorization is required, a browser tab opens with Arcade's OAuth page.
4. Complete the OAuth flow in the browser tab. Gleipnir polls Arcade in the background and flips the badge to **✓ Authorized** within ~60 seconds.
5. Repeat for each toolkit.

**Expect one OAuth screen per distinct scope, not per toolkit.** Arcade authorizes tools one at a time. When multiple tools share a scope, Arcade auto-completes the rest after the first grant and Gleipnir advances silently. When tools require *different* scopes, a new OAuth screen appears for each one. Concrete expectations:

- **Google Calendar** — usually 1 click (all tools share `calendar.events`).
- **Gmail** — usually 4–5 clicks in succession (`gmail.send`, `gmail.readonly`, `gmail.modify`, `gmail.compose`, `gmail.labels`).
- **Slack, GitHub, etc.** — varies by toolkit; could be anywhere from 1 to several.

For sensitive Google scopes (Gmail especially), Google may also show an **"unverified app"** warning on each screen if the Arcade OAuth client isn't verified for your account. Click **Advanced → Go to ... (unsafe)** to proceed. Including this bypass, Gmail can be ~10 total clicks.

The popup chain is automatic — you do not need to click anything inside Gleipnir between popups. Just keep clicking through Google's screens until they stop appearing.

**Persistence:** Once authorized, OAuth tokens live with Arcade (server-side). Grants persist across Gleipnir restarts, machine changes, and key rotations until the upstream token is revoked.

## Step 6 — Verify with a manual run

1. Create a policy that grants one or more tools from an authorized toolkit.
2. Trigger a manual run.
3. Verify the run completes successfully and the tool results contain real data (not an OAuth redirect URL).

## Limitations

**Single shared `Arcade-User-ID` per server registration.** Gleipnir currently stores one Arcade gateway URL and one `Arcade-User-ID` per MCP server row. Every policy that grants tools from that server acts as the same Arcade user and therefore uses the same downstream OAuth connections. Per-policy or per-user credential scoping — where different policies could act as different Arcade end-users — is not yet supported and will be addressed in a follow-up issue.

If you need to act as two different Arcade users, register two separate MCP server entries pointing to the same gateway URL, each with a different `Arcade-User-ID` header value.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| Tool call returns an OAuth URL despite the UI showing ✓ Authorized | `Arcade-User-ID` header in Gleipnir does not match the user ID used during the OAuth flow | Re-authorize after correcting the `Arcade-User-ID` header value to the email used in Arcade. |
| Tool call returns `[TOOL_RUNTIME_FATAL] ... RefreshError` despite the UI showing ✓ Authorized | Arcade has a stale or invalidated OAuth token cached for this user_id; the pre-auth API still reports authorized because Arcade does not validate the token at check-time | Force a fresh OAuth flow: rotate `Arcade-User-ID` to a new value (treats it as a fresh user with no cached state) or revoke the user/grant in the Arcade dashboard, then re-authorize. |
| All toolkits stuck on `[Check →]` after clicking | API key is invalid or the gateway has no tools yet | Verify the `Authorization` header value is correct. Ensure tools were discovered (Step 4). |
| Test Connection fails with 401 | Wrong or expired API key | Regenerate the key in Arcade's Settings → API Keys, then update the `Authorization` header value in Gleipnir. |
| Toolkit row stays in ⚠ Action needed after completing OAuth | OAuth was completed with a different browser session or the tab was closed before Arcade confirmed completion | Click **Check →** again on the row to re-check the authorization status. |
| Need to re-authorize a toolkit | OAuth token was revoked in Arcade or scopes changed | Revoke the grant in the Arcade dashboard (Connected Accounts), then click **Check →** again to start a fresh OAuth flow. |

A scriptable bulk-authorization helper (for environments without UI access) is tracked separately.
