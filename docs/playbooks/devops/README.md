# Homelab DevOps operations

**Status:** Complete

## What it does

On a manual trigger, this agent carries out common homelab operations described in natural language by the operator. It can restart a Docker container on a remote host, resolve Proxmox VM or LXC issues, update a DNS record in Technitium, and add or modify a Caddy reverse-proxy route. Before executing any write operation, the agent waits for explicit operator approval — every shell command and config change is reviewed before it runs.

## Prerequisites

- A running Gleipnir instance (see main `README.md`).
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

In Gleipnir, go to **Settings → MCP Servers → Add Server** four times. Use the LAN IP of the host running the MCP containers:

| Name | URL |
|------|-----|
| `docker` | `http://<HOST_IP>:8201/` |
| `proxmox` | `http://<HOST_IP>:8202/` |
| `technitium` | `http://<HOST_IP>:8203/` |
| `caddy` | `http://<HOST_IP>:8204/` |

> **caddy-mcp transport:** Unlike the other three, `caddy-mcp` speaks the `httpstream` MCP transport natively — it does not wrap a stdio server. Gleipnir connects to it directly on port 8204 without supergateway in the path.

After adding each server, click **Discover**. Note the exact tool names returned — the policy YAML below uses representative names. If Discover returns different names (e.g. `list_containers` instead of `docker_list_containers`), update the `tool:` entries in the policy before saving.

## Step 5 — Create the policy

Go to **Policies → New Policy** and fill in the form. The YAML below is the payload the form produces.

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
  type: manual

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
    You are a homelab DevOps assistant. The operator will describe a task.
    Determine what needs to be done, read current state first, then execute.
    After each change, verify the outcome.

    Common patterns:

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

    If the task is ambiguous or would affect more than the operator described,
    use the feedback channel to clarify before proceeding.
  limits:
    max_tokens_per_run: 20000
    max_tool_calls_per_run: 25
  concurrency: skip
```

**Why these choices:**

- Read-only tools (`list_*`, `get_*`) have no approval gate so the agent can assess state without interrupting the operator. Write tools are approval-gated with explicit timeouts.
- `proxmox.execute_command` — the "break glass" tool for cases where none of the typed Proxmox tools cover the fix — always requires approval since it runs arbitrary commands on the hypervisor.
- `feedback.enabled: true` gives the agent `gleipnir.ask_operator` for ambiguous tasks (e.g. "which nginx container — there are three?") without failing the run (ADR-031).
- `concurrency: skip` prevents a second run from stacking behind a first that is waiting for approval. Concurrent runs on the same host could conflict.
- `caddy.get_caddy_config` is not approval-gated; it is a read that never modifies state. `update_caddy_config` is, because Caddy applies config changes live with no undo.
- Tools not listed in `capabilities.tools` do not exist from the agent's perspective (ADR-001). The agent cannot call tools it was not granted, regardless of what it reasons.

## Step 6 — Trigger a test run

1. In Gleipnir, go to **Policies → devops → Trigger**.
2. Enter a read-only task to verify connectivity first: *"List all running containers on the Docker host."*
3. The agent calls `docker.list_containers` — no approval required. Confirm the list looks correct.
4. Next, test a write: *"Restart the nginx container on the Docker host."* The agent will call `docker.list_containers`, then `docker.restart_container` (approve in the approval modal), then verify. Review each step in the run trace.

## Extensions

### Restrict Proxmox operations to specific nodes or VMs

Use parameter scoping (ADR-017) to prevent the agent from touching resources outside the intended scope. For example, to restrict `proxmox.restart_vm` to a specific VM ID:

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
| Gleipnir cannot reach MCP servers | Wrong IP or ports not listening | Confirm MCP containers are up with `docker compose ps`. Test connectivity from the Gleipnir host: `curl http://<HOST_IP>:8201/`. |
| Tool names in policy don't match Discover output | MCP server updated its tool names | Click Discover again in Settings → MCP Servers and update `tool:` entries in the policy. |
