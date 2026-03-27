# Using stdio MCP servers with Gleipnir

Gleipnir communicates with MCP servers exclusively over HTTP transport. Many community MCP servers only support stdio transport (they read JSON-RPC from stdin and write to stdout). To use these servers with Gleipnir, run them behind [Supergateway](https://github.com/supercorp-ai/supergateway) — a lightweight open-source proxy (MIT license) that wraps a stdio process and exposes it over HTTP.

## How it works

```
Gleipnir ──HTTP──▶ Supergateway ──stdio──▶ MCP Server process
                   (sidecar)               (stdin/stdout)
```

Supergateway spawns the MCP server as a child process, translates JSON-RPC over HTTP into JSON-RPC over stdin/stdout, and translates the responses back. From Gleipnir's perspective, it looks like any other HTTP MCP server.

## Adding a stdio MCP server to Docker Compose

Suppose you want to use a stdio-only MCP server called `my-mcp-server`. Add a service to `docker-compose.yml`:

```yaml
services:
  # ... existing services ...

  my-mcp:
    image: node:22-alpine
    command: >
      npx -y supergateway
      --port 8093
      --stdio "npx -y @example/my-mcp-server"
    ports:
      - "8093:8093"
    environment:
      - MY_API_KEY=${MY_API_KEY}
    restart: unless-stopped
```

Key points:

- **`--stdio`** is the command supergateway runs as a child process. It can be any executable — `npx`, a Python script, a compiled binary, etc.
- **Environment variables** set on the container are inherited by the stdio child process. This is how you pass API keys and credentials to the underlying MCP server — the same pattern as the existing HTTP MCP servers in this stack.
- **Port** can be any unused port. Pick one and be consistent between the `command`, `ports`, and the URL you register in Gleipnir.

If the MCP server is distributed as a Docker image rather than an npm package, use a multi-stage or custom Dockerfile:

```dockerfile
FROM node:22-alpine AS base
RUN npm install -g supergateway

FROM your-org/your-mcp-server:latest
COPY --from=base /usr/local/lib/node_modules /usr/local/lib/node_modules
COPY --from=base /usr/local/bin/supergateway /usr/local/bin/supergateway
ENTRYPOINT ["supergateway", "--port", "8093", "--stdio", "/usr/local/bin/mcp-server"]
```

## Registering in Gleipnir

Once the sidecar is running, register it in Gleipnir exactly like any HTTP MCP server:

1. Go to **Settings → MCP Servers → Add Server**
2. Name: `my-mcp` (or whatever you prefer)
3. URL: `http://my-mcp:8093/` (use the Docker Compose service name as the hostname)
4. Click **Discover** to pull the tool list

From this point on, the server's tools are available for policies. Tag them as sensors/actuators/feedback and reference them in policy YAML using the usual dot notation (e.g., `my-mcp.some_tool`).

## Credentials and auth

Supergateway is a transport bridge, not a security layer:

- **Credentials for the managed asset** (API keys, tokens) are passed as environment variables on the Docker container, inherited by the stdio child process. This is the same model as the existing HTTP MCP servers in this stack.
- **No auth between Gleipnir and Supergateway.** Any container on the same Docker Compose network can reach the Supergateway port. In a homelab Compose deployment this is acceptable — the network boundary is your trust boundary. If you need inbound auth, place a reverse proxy (e.g., nginx) in front of Supergateway.
- **No per-tool credential scoping.** The MCP server has access to whatever credentials you give it, across all its tools. Gleipnir's capability enforcement controls which tools the agent can call, but cannot restrict what the MCP server does internally.

## Example: wrapping a Python stdio MCP server

```yaml
  github-mcp:
    image: node:22-alpine
    command: >
      sh -c "apk add --no-cache python3 py3-pip &&
             pip install --break-system-packages github-mcp-server &&
             npx -y supergateway --port 8094 --stdio 'github-mcp-server'"
    ports:
      - "8094:8094"
    environment:
      - GITHUB_TOKEN=${GITHUB_TOKEN}
    profiles:
      - integrations
    restart: unless-stopped
```

Then register in Gleipnir as `github`, URL `http://github-mcp:8094/`.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Container exits immediately | Supergateway can't find or start the stdio command | Check `docker compose logs <service>`. Verify the MCP server package name/path is correct. |
| Discovery returns 0 tools | Supergateway started but the MCP server didn't complete the JSON-RPC handshake | Check if the MCP server needs environment variables to initialize (e.g., an API key). |
| Tools fail at runtime | Missing credentials in the environment | Verify the env vars are set in `.env` and listed in the service's `environment:` block. |
| Connection refused from Gleipnir | Wrong port or service name | Ensure the port in `--port`, `ports:`, and the registered URL all match. Use the Compose service name (not `localhost`) as the hostname. |
