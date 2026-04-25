# Connect Gleipnir to Composio

**Status:** Complete

## What Composio is

[Composio](https://composio.dev) is a hosted MCP provider that exposes 250+ third-party integrations (Gmail, Slack, GitHub, Google Calendar, Linear, and many others) as a single MCP server per tool category. Instead of running your own MCP server for each integration, you point Gleipnir at Composio's hosted endpoint and use your Composio API key to authenticate.

### Trust expansion warning

When you connect Gleipnir to Composio, **downstream OAuth tokens for services like Gmail and Slack live with Composio, not with Gleipnir.** Any policy whose capabilities include a Composio-provided tool can instruct the agent to read your email, send Slack messages, or take other account-level actions on the connected service — within the scope of what that OAuth token permits.

This is an intentional trust expansion. Review the tools you grant to each policy carefully. Only grant the specific tools a policy actually needs, and consider using `approval: required` on write operations (e.g. `gmail.send_email`, `slack.send_message`) so you can review every action before it executes.

## Prerequisites

- A running Gleipnir instance.
- A Composio account. Sign up at [app.composio.dev](https://app.composio.dev).
- At least one integration connected in the Composio dashboard (e.g. Gmail, GitHub).

## Step 1 — Get your Composio API key

1. Log in to [app.composio.dev](https://app.composio.dev).
2. Go to **Settings → API Keys**.
3. Copy your API key — it starts with `sk_live_`.

## Step 2 — Find your MCP server URL

Composio exposes each tool category as a separate MCP server. The URL template is:

```
https://backend.composio.dev/v3/mcp/<server_id>?user_id=<user_id>
```

- **`server_id`** — identifies the tool category (e.g. `gmail`, `github`, `slack`, `googlecalendar`). Find the exact value in the Composio dashboard under **MCP Servers** or in the [Composio docs](https://docs.composio.dev/mcp).
- **`user_id`** — your Composio user ID, also found in **Settings → Account** in the dashboard. This is the Composio account that owns the downstream OAuth connections.

Example for Gmail:

```
https://backend.composio.dev/v3/mcp/gmail?user_id=user_abc123
```

## Step 3 — Register the MCP server in Gleipnir

1. In Gleipnir, go to **Settings → MCP Servers → Add Server**.
2. Fill in the fields:
   - **Name:** `composio-gmail` (or whatever name makes sense for the integration)
   - **URL:** paste your full Composio MCP URL from Step 2
3. Under **Auth Headers**, click **Auth headers** to open the header editor, then click **+ Add header**:
   - **Name:** `x-api-key`
   - **Value:** paste your Composio API key (`sk_live_...`)
4. Click **Test Connection**. Gleipnir will call the Composio server and return a list of discovered tools. If the test fails, double-check the URL and API key.
5. Click **Save**.

The API key is stored encrypted in the Gleipnir database using AES-256-GCM. It is never returned in API responses — only the header name (`x-api-key`) is visible after saving.

To update the API key later, open the server's detail panel, click **Auth headers**, type the new value in the value field for the existing `x-api-key` row (the name field is read-only), and click **Save**. To remove the header entirely, click the trash icon for that row before saving.

## Step 4 — Discover tools

After saving, click **Discover** on the server. Gleipnir fetches the current tool list from Composio and shows the names and input schemas. Note the exact tool names — you will reference them in your policy YAML.

## Step 5 — Create a policy

Go to **Policies → New Policy**. Grant only the tools the policy needs. Here is a minimal example that lets the agent read Gmail messages and draft a summary:

```yaml
name: gmail-daily-summary
description: Read unread Gmail messages and summarize them.
folder: Email

model:
  provider: anthropic
  name: claude-sonnet-4-6

trigger:
  type: manual

capabilities:
  tools:
    - tool: composio-gmail.gmail_fetch_emails
    - tool: composio-gmail.gmail_list_threads
    - tool: composio-gmail.gmail_send_email
      approval: required
      timeout: 30m
      on_timeout: reject
  feedback:
    enabled: true
    timeout: 30m
    on_timeout: fail

agent:
  task: |
    Fetch the 10 most recent unread emails from the inbox. For each one,
    extract the sender, subject, and a one-sentence summary of the body.
    Present the results as a numbered list. If you need to send a reply,
    use the feedback channel to ask the operator to review the draft first.

  limits:
    max_tokens_per_run: 20000
    max_tool_calls_per_run: 20
  concurrency: discard
```

Adjust the tool names to match what Discover returned for your server. Composio tool names follow the pattern `<server_id>.<verb>_<noun>`.

## Limitations

**Single shared `user_id` per server registration.** Gleipnir currently stores one Composio MCP URL (including its `user_id` query parameter) per MCP server row. Every policy that grants tools from that server acts as the same Composio user and therefore uses the same downstream OAuth connections. Per-policy or per-user credential scoping — where different policies could act as different Composio end-users — is not yet supported and will be addressed in a follow-up issue.

If you need to act as two different Composio users, register two separate MCP servers with different `user_id` values in their URLs, and grant each policy tools from the appropriate server.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| Test Connection returns 401 | Wrong or expired API key | Regenerate the key at app.composio.dev → Settings → API Keys. In Gleipnir, open the server, click **Auth headers**, then type the new value in the existing row's value field and click **Save**. |
| Test Connection returns 404 | Wrong `server_id` in URL | Check the exact server ID in the Composio dashboard under MCP Servers and correct the URL. |
| Discover returns 0 tools | Integration not connected in Composio | Log in to app.composio.dev, go to Apps, and connect the integration. Then click Discover again. |
| Tool call returns "permission denied" | OAuth scope missing | In Composio, go to Connected Accounts and check that the required scopes are granted for the integration. |
| Tool names in policy don't match Discover output | Composio updated tool names | Click Discover again and update the `tool:` entries in the policy YAML to match the current names. |
