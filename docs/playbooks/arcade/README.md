# Connect Gleipnir to Arcade

**Status:** Complete

## What Arcade is

[Arcade](https://arcade.dev) is a hosted MCP provider that exposes dozens of third-party integrations (Gmail, Google Calendar, Slack, GitHub, Linear, and many others) behind a single MCP gateway. Instead of running your own MCP server for each integration, you create a gateway in the Arcade dashboard, connect your downstream accounts (e.g. Google) through Arcade's hosted OAuth flow, and point Gleipnir at the gateway URL.

### Trust expansion warning

When you connect Gleipnir to Arcade, **downstream OAuth tokens for services like Gmail and Google Calendar live with Arcade, not with Gleipnir.** Any policy whose capabilities include an Arcade-provided tool can instruct the agent to read your email, modify your calendar, or take other account-level actions within the scope of the OAuth token.

This is an intentional trust expansion. Grant each policy only the specific tools it needs, and consider `approval: required` on any write operation (e.g. `Gmail.SendEmail`, `GoogleCalendar.CreateEvent`, `GoogleCalendar.DeleteEvent`) so you can review every action before it executes.

## Prerequisites

- A running Gleipnir instance.
- An Arcade account. Sign up at [arcade.dev](https://arcade.dev) — the Hobby tier is $0 and sufficient for personal/homelab use.
- At least one integration connected in the Arcade dashboard (e.g. Google).

## Step 1 — Connect your downstream account in Arcade

**This step must be completed before you register the server in Gleipnir.** Arcade's MCP gateway expects the connected-account OAuth to be in place; a tool call made against an un-authorized account returns an authorization-required response that Gleipnir cannot handle inline.

1. Log in to [api.arcade.dev](https://api.arcade.dev) (the dashboard).
2. Go to **Auth** (or **Connected Accounts**) and click **Connect Google**. Arcade uses its own hosted Google OAuth app, so you do **not** need to create a Google Cloud project. Grant the Gmail and Google Calendar scopes during the consent screen.
3. After the redirect completes, the account shows as connected under your Arcade user (identified by the email you signed up with).

## Step 2 — Create a gateway

1. In the Arcade dashboard, open **Gateways → New Gateway**.
2. Give it a name (e.g. `gleipnir`).
3. Add the toolkits you want exposed. For this playbook that is **Gmail** and **Google Calendar** — add both so a single server registration in Gleipnir covers them.
4. Set the auth mode to **"Arcade Headers"** (not "Arcade Auth"). Gleipnir sends static headers per request and cannot participate in the dynamic OAuth/DCR handshake that Arcade Auth mode uses.
5. Save. Copy the gateway URL from the dashboard — it looks like:
   ```
   https://api.arcade.dev/mcp/gateway/<gateway-id>
   ```

## Step 3 — Get your Arcade API key

1. In the Arcade dashboard, go to **Settings → API Keys → New key**.
2. Copy the key — it starts with `arc_`. Treat it like a password; anyone with it can call your gateway and act against your connected Google account.

## Step 4 — Register the MCP server in Gleipnir

1. In Gleipnir, go to **Settings → MCP Servers → Add Server**.
2. Fill in the fields:
   - **Name:** `arcade-google` (or another name you prefer — the name becomes the prefix in policy tool references, e.g. `arcade-google.GoogleCalendar.ListEvents`).
   - **URL:** paste the gateway URL from Step 2.
3. Click **Auth headers** to open the header editor, then click **+ Add header** twice to add both of these:

   | Name | Value |
   |------|-------|
   | `Authorization` | `Bearer arc_...` (your Arcade API key from Step 3, prefixed with `Bearer ` and a space) |
   | `Arcade-User-ID` | the email address you used to sign up for Arcade (this is the Arcade user whose connected Google account the tools will run against) |

4. Click **Test Connection**. Gleipnir will call the Arcade gateway and return a list of discovered tools. If the test fails, double-check the URL, Bearer prefix, and user ID.
5. Click **Save**.

Both header values are stored encrypted in the Gleipnir database using AES-256-GCM. They are never returned in API responses — only the header names are visible after saving. See [ADR-039](../../developer/ADR_Tracker.md) for the storage details.

To update the API key later, open the server's detail panel, click **Auth headers**, type the new value into the existing `Authorization` row (the name field is read-only), and save.

## Step 5 — Discover tools

After saving, click **Discover** on the server. Gleipnir fetches the current tool list from Arcade and shows the names and input schemas. Note the exact tool names — you will reference them in your policy YAML.

Arcade uses PascalCase, dotted namespaces: `Gmail.SendEmail`, `GoogleCalendar.ListEvents`, and so on. In Gleipnir policies, reference them with the server name prefix: `arcade-google.Gmail.SendEmail`, `arcade-google.GoogleCalendar.ListEvents`. Gleipnir splits only on the first dot, so the rest of the name (including its own internal dot) stays intact.

## Step 6 — Smoke-test the connection

Before creating a real policy, verify the end-to-end path with a read-only tool. Arcade exposes `Gmail.WhoAmI` and `GoogleCalendar.WhoAmI` — both return the connected Google account's email and require no meaningful scope beyond the initial OAuth grant.

1. Create a throwaway policy `arcade-smoke-test` with trigger `manual`, capabilities `arcade-google.GoogleCalendar.WhoAmI` (no approval gate), and a task prompt like:
   ```
   Call GoogleCalendar.WhoAmI and report the email address it returns.
   ```
2. Trigger a run. The run should complete and report the email address you used in Step 1.
3. If the call returns an authorization-required response instead, Arcade has not received a valid Google connection for this user. Revisit Step 1 — disconnect and reconnect Google in the Arcade dashboard, granting all requested scopes — and retry.

Once the smoke test passes, delete the policy and move on to your real use case. See the [meal-planning playbook](../meal-planning/README.md) for a complete Google-Calendar-driven example.

## Limitations

**Single Arcade end-user per server registration.** Gleipnir currently stores one set of auth headers per MCP server row, which includes one fixed `Arcade-User-ID` value. Every policy that grants tools from this server acts as the same Arcade user and therefore uses the same downstream OAuth connections. Per-policy or per-user credential scoping — where different policies could act as different end-users — is not supported.

If you need to act as two different Arcade users, register two separate MCP servers in Gleipnir, each with a different `Arcade-User-ID` header, and grant each policy tools from the appropriate server.

**No dynamic auth / URL elicitation.** Arcade's "Arcade Auth" gateway mode returns authorization URLs the client is expected to open when tool calls hit an unauthorized account. Gleipnir cannot handle that flow. Always use **Arcade Headers** mode and complete Google (and any other) OAuth connections in the Arcade dashboard before running a policy. If a downstream connection expires, runs will fail with an auth-required response rather than waiting — reconnect in the Arcade dashboard to recover.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| Test Connection returns 401 | Wrong or expired Arcade API key, or missing `Bearer ` prefix | Regenerate the key in the Arcade dashboard → Settings → API Keys. In Gleipnir, update the `Authorization` header value to `Bearer <new key>` (note the space). |
| Test Connection returns 404 | Wrong gateway URL | Copy the exact gateway URL from the Arcade dashboard → Gateways. |
| Discover returns 0 tools | Gateway has no toolkits, or gateway uses "Arcade Auth" mode instead of "Arcade Headers" | In the Arcade dashboard, open the gateway and confirm it has toolkits attached and the auth mode is **Arcade Headers**. |
| Smoke-test `WhoAmI` returns an authorization URL | Google not connected for this Arcade user | Go to Arcade dashboard → Auth → Google → reconnect, granting the Gmail and Calendar scopes. |
| Tool call returns "permission denied" mid-run | OAuth scope missing for the specific action | Disconnect Google in the Arcade dashboard and reconnect, accepting all scopes. Some toolkits require additional scopes that are only requested on reconnection. |
| Tool names in policy don't match Discover output | Arcade updated a toolkit's tool names, or a toolkit was removed from the gateway | Click Discover again on the server in Gleipnir and update the `tool:` entries in affected policies to match. |
