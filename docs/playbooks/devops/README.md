# Homelab DevOps operations

**Status:** Complete

## What it does

When [Uptime Kuma](https://github.com/louislam/uptime-kuma) detects that a monitored service has gone **down**, it fires a webhook at Gleipnir, which triggers this agent. The agent reads the Uptime Kuma heartbeat payload to identify which service failed and why, then carries out common homelab remediations: restart a Docker container on a remote host, resolve a Proxmox VM or LXC issue, update a DNS record in Technitium, or add/modify a Caddy reverse-proxy route. Before executing any write operation, the agent waits for explicit operator approval — every shell command and config change is reviewed before it runs.

The webhook trigger filters on the heartbeat status, so only **down** events launch a run; Uptime Kuma's recovery (up) and test pings are accepted and discarded without spending tokens. The agent never touches anything on its own — it only acts in response to a real outage Uptime Kuma reported, and even then only after you approve each change.

## Prerequisites

- A running Gleipnir instance (see main `README.md`), reachable from the Uptime Kuma host (Uptime Kuma must be able to POST to Gleipnir's webhook URL).
- A running [Uptime Kuma](https://github.com/louislam/uptime-kuma) instance with at least one monitor watching a service you want auto-remediated.
- Docker and Docker Compose on the same host as Gleipnir, with Go 1.24+ available during the `caddy-mcp` build (the multi-stage build handles this; Go does not need to be installed on the host).
- SSH access from the Gleipnir host to the remote Docker host, using a private key (the `docker-mcp` service connects via `DOCKER_HOST=ssh://`).
- A Proxmox VE instance with API token authentication configured.
- A Technitium DNS Server instance with the HTTP API enabled (**Settings → Web Service → Enable DNS Server HTTP/HTTPS API**).
- A Caddy instance with the admin API reachable from the Gleipnir host (default: `http://<caddy-host>:2019`).

## MCP servers used

| Server | Purpose | Source | Auth |
|--------|---------|--------|------|
| `docker-mcp` | Restart and inspect containers on a remote Docker host | [QuantGeekDev/docker-mcp](https://github.com/QuantGeekDev/docker-mcp) | SSH private key via `DOCKER_HOST=ssh://` |
| `proxmox-mcp` | Manage VMs and LXC containers via Proxmox REST API | [canvrno/ProxmoxMCP](https://github.com/canvrno/ProxmoxMCP) | Proxmox API token |
| `technitium-mcp` | Manage DNS zones and records | [rosschurchill/technitium-mcp-secure](https://github.com/rosschurchill/technitium-mcp-secure) | Technitium API token |
| `caddy-mcp` | Read and update Caddy routing config | [lum8rjack/caddy-mcp](https://github.com/lum8rjack/caddy-mcp) | None (admin API on trusted network) |

## Step 1 — Set up credentials

### Docker: SSH key

`docker-mcp` connects to the remote Docker daemon using Docker's native SSH transport (`DOCKER_HOST=ssh://user@host`). The container needs a passphrase-free private key.

Generate a dedicated key pair if you do not already have one:

```bash
ssh-keygen -t ed25519 -f ./id_ed25519 -N "" -C "gleipnir-devops"
```

Run this from `docs/playbooks/devops/` — the Compose file mounts `id_ed25519` from that directory.

Copy the public key to the Docker host:

```bash
ssh-copy-id -i ./id_ed25519.pub user@<docker-host>
```

Add the host fingerprint to a local `known_hosts` file:

```bash
ssh-keyscan <docker-host> >> ./known_hosts
```

Verify the connection works without prompts:

```bash
ssh -i ./id_ed25519 -o UserKnownHostsFile=./known_hosts user@<docker-host> "docker ps"
```

### Proxmox: API token

`proxmox-mcp` uses Proxmox's token-based authentication — no SSH access to the Proxmox host is required.

1. In the Proxmox web UI, go to **Datacenter → Permissions → API Tokens → Add**.
2. Select the user (e.g. `root@pam`), give the token an ID (e.g. `gleipnir`), and uncheck **Privilege Separation** if you want the token to inherit the user's full permissions. Click **Add**.
3. Copy both the **Token ID** (format: `user@realm!tokenid`) and the **Token Secret** — the secret is shown only once.

The four environment variables `proxmox-mcp` expects:

| Variable | Example |
|----------|---------|
| `PROXMOX_HOST` | `192.168.1.10` |
| `PROXMOX_USER` | `root@pam!gleipnir` |
| `PROXMOX_TOKEN_NAME` | `gleipnir` |
| `PROXMOX_TOKEN_VALUE` | `xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx` |

### Technitium: API token

1. Log in to the Technitium web UI as an admin.
2. Go to **Administration → Sessions → Create API Token**.
3. Give it a name (e.g. `gleipnir`) and click **Create**. Copy the token — shown only once.

### Caddy: admin URL

No auth required by default. Note the full URL to the Caddy admin API, e.g. `http://192.168.1.20:2019`. Restrict access at the network level; do not expose port 2019 to untrusted networks.

## Step 2 — Create .env

Create `.env` inside `docs/playbooks/devops/` — the same directory as `docker-compose.yml`:

```
# Docker remote host — full ssh:// URL for DOCKER_HOST
DOCKER_HOST=ssh://user@<docker-host>

# Proxmox API token
PROXMOX_HOST=<proxmox-host-ip>
PROXMOX_USER=root@pam!gleipnir
PROXMOX_TOKEN_NAME=gleipnir
PROXMOX_TOKEN_VALUE=<paste token secret>
PROXMOX_VERIFY_SSL=false

# Technitium DNS
TECHNITIUM_BASE_URL=http://<technitium-host>:<port>
TECHNITIUM_TOKEN=<paste Technitium API token>

# Caddy admin API
CADDY_ADMIN_URL=http://<caddy-host>:2019
```

`id_ed25519` and `known_hosts` are file mounts — they do not go in `.env`.

Do not commit `.env`, `id_ed25519`, or `id_ed25519.pub`. All three are listed in `.gitignore` at the repo root.

## Step 3 — Build and start the MCP servers

The `docker-mcp` and `proxmox-mcp` services share a custom base image (`Dockerfile.python-mcp`) that adds Python and `uvx` on top of a Node.js image. `caddy-mcp` is compiled from source in a multi-stage Go build (`Dockerfile.caddy-mcp`). The `--build` flag is required on first start.

```bash
cd docs/playbooks/devops
docker compose up -d --build
```

The first run downloads Go modules and npm packages; subsequent starts reuse the built images. Verify all four services are up:

```bash
docker compose ps
```

All should show `Up`. If any shows `Exited`, check its logs:

```bash
docker compose logs docker-mcp
docker compose logs proxmox-mcp
docker compose logs technitium-mcp
docker compose logs caddy-mcp
```

## Step 4 — Register each MCP server in Gleipnir

In Gleipnir, go to **Tools → Add MCP server** four times. Use the LAN IP of the host running the MCP containers:

| Name | URL |
|------|-----|
| `docker` | `http://<HOST_IP>:8201/mcp` |
| `proxmox` | `http://<HOST_IP>:8202/mcp` |
| `technitium` | `http://<HOST_IP>:8203/mcp` |
| `caddy` | `http://<HOST_IP>:8204/` |

> **Transports:** The first three wrap stdio MCP servers via supergateway with `--outputTransport streamableHttp`, which serves the streamable-HTTP transport at `/mcp`. `caddy-mcp` is a Go binary speaking the `httpstream` MCP transport natively — Gleipnir connects to it directly on port 8204 at the root path without supergateway in the path.

After adding each server, click **Discover**. Note the exact tool names returned — the policy YAML below uses representative names. If Discover returns different names (e.g. `list_containers` instead of `docker_list_containers`), update the `tool:` entries in the policy before saving.

## Step 5 — Create the policy

Go to **Agents → New Agent** and fill in the form. The YAML below is the payload the form produces.

Read-only tools have no approval gate so the agent can gather state freely. Write tools (`restart_*`, `update_*`, `execute_*`) require approval — the operator reviews the exact parameters before the MCP server is called.

> **Tool names are representative.** Run Discover for each server and update the `tool:` entries to match the names it returns before saving the policy.

```yaml
name: devops
description: Homelab DevOps operations — restart Docker containers, fix Proxmox issues, update Technitium DNS records, and reconfigure Caddy routes.
folder: Infrastructure

model:
  provider: anthropic
  name: claude-sonnet-4-6
  options:
    enable_prompt_caching: true

trigger:
  type: webhook
  # Uptime Kuma can attach a static Authorization header but cannot HMAC-sign
  # the body, so use bearer auth (not the hmac default). Reveal the generated
  # secret on the policy detail page and paste it into Uptime Kuma in Step 6.
  auth: bearer
  match: all
  checks:
    # Only fire on DOWN events. Uptime Kuma sends status: 1 on recovery and a
    # test ping when you click "Test"; both fail this check and are discarded
    # with 200 {"data":{"filtered":true}} without launching a run.
    - path: "$.heartbeat.status"
      equals: 0

capabilities:
  tools:
    # --- Docker ---
    - tool: docker.list_containers
    - tool: docker.get_logs
    - tool: docker.restart_container
      approval: required
      timeout: 2m
      on_timeout: reject

    # --- Proxmox ---
    - tool: proxmox.get_nodes
    - tool: proxmox.get_vms
    - tool: proxmox.get_containers
    - tool: proxmox.restart_vm
      approval: required
      timeout: 5m
      on_timeout: reject
    - tool: proxmox.restart_container
      approval: required
      timeout: 5m
      on_timeout: reject
    - tool: proxmox.execute_command
      approval: required
      timeout: 2m
      on_timeout: reject

    # --- Technitium DNS ---
    - tool: technitium.list_zones
    - tool: technitium.list_records
    - tool: technitium.update_record
      approval: required
      timeout: 30s
      on_timeout: reject

    # --- Caddy ---
    - tool: caddy.get_caddy_config
    - tool: caddy.update_caddy_config
      approval: required
      timeout: 30s
      on_timeout: reject

  feedback:
    enabled: true
    timeout: 30m
    on_timeout: fail

agent:
  task: |
    You are a homelab DevOps assistant. Uptime Kuma has reported that a
    monitored service is DOWN. The webhook payload is delivered as your first
    message — it is the Uptime Kuma notification JSON. Read these fields:

      $.monitor.name  — the human name of the failed service (e.g. "Plex")
      $.monitor.url   — the URL or host Uptime Kuma was probing
      $.monitor.type  — the check type (http, port, ping, docker, ...)
      $.heartbeat.msg — the failure reason (e.g. "connect ECONNREFUSED ...")

    Use these to identify which underlying resource is down, then read current
    state before making any change, and verify the outcome after each change.
    Do not act beyond restoring the reported service. If the payload is
    ambiguous about which resource to touch (e.g. the monitor name does not
    clearly map to one container or VM), use the feedback channel to ask the
    operator before proceeding rather than guessing.

    Map the failure to the right remediation and follow that pattern:

    Restart a Docker container:
      1. docker.list_containers — find the container and confirm it exists.
      2. docker.restart_container — restart it (requires approval).
      3. docker.list_containers — confirm it is running.
      4. docker.get_logs — check for errors in the first few seconds after start.

    Fix a Proxmox issue:
      1. proxmox.get_nodes / get_vms / get_containers — read current state.
      2. Identify the affected resource and the correct fix.
      3. Use restart_vm, restart_container, or execute_command as needed
         (each requires approval). Prefer targeted restarts over node reboots.

    Update a Technitium DNS record:
      1. technitium.list_zones — confirm the zone name.
      2. technitium.list_records — read the current record value.
      3. technitium.update_record — apply the change (requires approval).
      4. technitium.list_records — verify the new value is set.

    Update a Caddy route:
      1. caddy.get_caddy_config — read the full current config.
      2. Identify the route to add or modify.
      3. caddy.update_caddy_config — apply a targeted patch (requires approval).
         Avoid replacing the entire config; prefer the smallest targeted change.
      4. caddy.get_caddy_config — confirm the route is present and correct.

    If no remediation pattern fits the reported failure, or the fix would
    affect more than the single service Uptime Kuma flagged, use the feedback
    channel to clarify before proceeding.
  limits:
    max_tokens_per_run: 20000
    max_tool_calls_per_run: 25
  concurrency: skip
```

**Why these choices:**

- `trigger.auth: bearer` — Uptime Kuma's Webhook notification can attach a static `Authorization` header but cannot HMAC-sign the body, so the `hmac` default would reject every request. Bearer validates a fixed secret header, which Uptime Kuma can send. Do not use `auth: none` — the webhook URL is unauthenticated otherwise and anyone who learns the policy ID could trigger remediation runs.
- The `checks` filter on `$.heartbeat.status == 0` is a hard pre-run gate: recovery (`status: 1`) and Uptime Kuma test pings are filtered out before a run launches, so the agent only ever wakes up for a real outage. This matters more than for a manual policy because nothing else stands between the webhook and an approval-gated write.
- Read-only tools (`list_*`, `get_*`) have no approval gate so the agent can assess state without interrupting the operator. Write tools are approval-gated with explicit timeouts.
- `proxmox.execute_command` — the "break glass" tool for cases where none of the typed Proxmox tools cover the fix — always requires approval since it runs arbitrary commands on the hypervisor.
- `feedback.enabled: true` gives the agent `gleipnir.ask_operator` for ambiguous tasks (e.g. "which nginx container — there are three?") without failing the run.
- `concurrency: skip` prevents a second run from stacking behind a first that is waiting for approval. Concurrent runs on the same host could conflict.
- `caddy.get_caddy_config` is not approval-gated; it is a read that never modifies state. `update_caddy_config` is, because Caddy applies config changes live with no undo.
- Tools not listed in `capabilities.tools` do not exist from the agent's perspective. The agent cannot call tools it was not granted, regardless of what it reasons.

## Step 6 — Connect Uptime Kuma

### Get the webhook URL and secret from Gleipnir

The URL and secret only exist once the policy is saved, so do this after Step 5. Reopen the **devops** agent in the editor (**Agents → devops**) and scroll to the **Trigger** section:

1. **Webhook URL** is shown read-only — click **Copy**. It looks like `https://<gleipnir-host>/api/v1/webhooks/<policy-id>`.
2. Under **Authentication mode**, confirm **Bearer token** is selected (it is, from the YAML in Step 5).
3. Under **Shared secret**, click **Generate initial secret** the first time (or **Show** if one already exists), then **Copy** it. Admin or operator role is required. Treat the secret like a password — anyone with the URL and secret can fire a remediation run.

The editor also renders a ready-to-paste `curl` command for the current auth mode just below the secret — handy for the test in Step 7.

### Create the Webhook notification in Uptime Kuma

1. In Uptime Kuma, go to **Settings → Notifications → Setup Notification**.
2. Set **Notification Type** to **Webhook**.
3. **Post URL:** the Gleipnir webhook URL from above.
4. **Request Body:** select **Preset - application/json**. Uptime Kuma's default JSON body includes the `heartbeat` and `monitor` objects this policy reads.
5. **Additional Headers:** add the bearer token as JSON:
   ```json
   { "Authorization": "Bearer <paste the policy secret>" }
   ```
6. Save the notification.

### Assign the notification to your monitors

Edit each monitor you want auto-remediated (**Edit → Notifications**) and enable the new notification. Only assign it to services this agent actually knows how to fix — a monitor for an external website the agent cannot touch will trigger a run that can only fail or ask for feedback.

> **Why bearer, not HMAC:** Gleipnir's webhook default is `auth: hmac`, which requires the sender to sign each body with a shared secret. Uptime Kuma cannot compute that signature — it can only attach static headers — so the policy uses `auth: bearer` and Uptime Kuma sends a fixed `Authorization: Bearer` header instead. See ADR-034 for how the secret is stored.

## Step 7 — Trigger a test run

You can exercise the full path without waiting for a real outage by POSTing a representative Uptime Kuma down payload directly to the webhook. Run this from a host that can reach Gleipnir:

```bash
curl -X POST https://<gleipnir-host>/api/v1/webhooks/<policy-id> \
  -H "Authorization: Bearer <policy-secret>" \
  -H "Content-Type: application/json" \
  -d '{
        "heartbeat": { "status": 0, "msg": "connect ECONNREFUSED 10.0.0.5:32400", "time": "2026-06-07 03:14:00", "important": true },
        "monitor": { "name": "nginx", "url": "http://10.0.0.5", "type": "http" },
        "msg": "[nginx] [🔴 Down] connect ECONNREFUSED 10.0.0.5:32400"
      }'
```

Expected: `202 Accepted` with `{"data":{"run_id":"..."}}` and a new run appears in Gleipnir. Open the run trace — the agent should read `$.monitor.name` ("nginx"), call `docker.list_containers`, then request approval for `docker.restart_container`. Approve it in the modal and confirm the agent verifies the restart.

Then prove the filter works: resend the same request with `"status": 1`. Expected: `200 {"data":{"filtered":true}}` and **no** new run — recovery pings are discarded.

Finally, for an end-to-end check, stop a real monitored service and let Uptime Kuma fire the notification on its own. Confirm the run launches from Uptime Kuma's request, not just your `curl`.

## Extensions

### Restrict Proxmox operations to specific nodes or VMs

Use parameter scoping to prevent the agent from touching resources outside the intended scope. For example, to restrict `proxmox.restart_vm` to a specific VM ID:

```yaml
- tool: proxmox.restart_vm
  approval: required
  timeout: 5m
  on_timeout: reject
  params:
    - name: vmid
      allowed: ["100", "101"]
```

The agent cannot pass any other `vmid` value even if it reasons its way there — the parameter constraint is enforced by the runtime before the MCP server is called, not by the prompt.

### Restrict Caddy updates to specific routes

If you want to prevent the agent from rewriting unrelated routes, scope `update_caddy_config` to a specific path prefix in the Caddy config tree:

```yaml
- tool: caddy.update_caddy_config
  approval: required
  timeout: 30s
  on_timeout: reject
  params:
    - name: path
      pattern: "^/apps/http/servers/srv0/routes/.*"
```

### Scheduled DNS health check

A separate cron policy can alert you when a DNS record drifts from an expected value — for example, detecting if a dynamic IP changes unexpectedly:

```yaml
name: dns-drift-check
folder: Infrastructure
trigger:
  type: cron
  cron_expr: "0 * * * *"   # hourly
capabilities:
  tools:
    - tool: technitium.list_records
  feedback:
    enabled: true
    timeout: 1h
    on_timeout: fail
agent:
  task: |
    Check that home.example.com resolves to 203.0.113.42.
    Use technitium.list_records to read the current A record for that zone.
    If the IP has changed, alert the operator via the feedback channel with
    the current and expected values. If it matches, complete silently.
  limits:
    max_tokens_per_run: 4000
    max_tool_calls_per_run: 5
  concurrency: skip
```

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| `docker-mcp` fails with "connection refused" or SSH error | Remote Docker host not reachable or SSH key not authorized | Run `ssh -i ./id_ed25519 -o UserKnownHostsFile=./known_hosts user@<docker-host> docker ps` directly to isolate the SSH issue before blaming docker-mcp. |
| `docker-mcp` fails with "host key verification failed" | Target host not in `known_hosts` | Run `ssh-keyscan <docker-host> >> docs/playbooks/devops/known_hosts` and restart the container. |
| `proxmox-mcp` returns 401 | API token wrong or expired | Regenerate the token in Proxmox → Datacenter → Permissions → API Tokens. |
| `proxmox-mcp` returns 403 | Token lacks permission for the operation | Check the token's assigned role in Proxmox. Assign `PVEAdmin` or a custom role with the required privilege (e.g. `VM.PowerMgmt`). |
| `technitium-mcp` fails with "cannot find package" | `@rosschurchill/technitium-mcp-secure` not on the npm registry | Clone the repo, build locally, and update the Compose command to reference the built binary directly. |
| `technitium-mcp` returns 403 | API token invalid | Regenerate in Technitium → Administration → Sessions. |
| `caddy-mcp` build fails | `go install` cannot reach the module proxy | Check internet access from the build host. If offline, add `--network=host` or pre-cache the module. |
| `caddy-mcp` cannot reach Caddy | Caddy admin API bound to localhost only | On the Caddy host, change `admin localhost:2019` to `admin 0.0.0.0:2019` (or the Gleipnir host's LAN IP) in the Caddyfile and reload. |
| Gleipnir cannot reach MCP servers | Wrong IP or ports not listening | Confirm MCP containers are up with `docker compose ps`. Test connectivity from the Gleipnir host: `curl http://<HOST_IP>:8201/mcp`. |
| Tool names in policy don't match Discover output | MCP server updated its tool names | Click Discover again on the **Tools** page and update `tool:` entries in the policy. |
| Webhook returns `401` (credential absent) | No `Authorization` header reached Gleipnir | Add the header in Uptime Kuma's **Additional Headers** as `{"Authorization": "Bearer <secret>"}`. A proxy stripping the header will also cause this. |
| Webhook returns `403` (credential invalid) | Bearer token present but wrong | The secret does not match the policy's current secret — copy it again from the editor. If you clicked **Rotate** in Gleipnir, update Uptime Kuma with the new value. |
| Webhook returns `409 Conflict` | A run is already active and `concurrency: skip` | Expected when outages overlap — the second down event is intentionally dropped. Wait for the active run (which may be paused on approval) to finish. |
| Uptime Kuma fires but no run launches (`200 {"data":{"filtered":true}}`) | Payload did not match the `$.heartbeat.status == 0` check | Down events carry `status: 0`; recovery and test pings do not. Confirm you triggered a real down (not a "Test" click) and that the body preset is **application/json** so the `heartbeat` object is present. |
| Webhook returns `404 Not Found` | Wrong policy ID in the URL, or the policy was deleted | Copy the URL fresh from the policy detail page. |
| Webhook returns `400 Bad Request` | Body is not valid JSON | In Uptime Kuma set Request Body to **Preset - application/json**, not a custom non-JSON body. |
| Run launches but agent can't map the monitor to a resource | `$.monitor.name` doesn't match a container/VM name | The agent will ask via the feedback channel. To automate, name your Uptime Kuma monitors to match the underlying Docker container or Proxmox VM, or add parameter scoping (see Extensions). |
