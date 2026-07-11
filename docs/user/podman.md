# Running Gleipnir under Podman

Gleipnir runs under [Podman](https://podman.io/) (rootless, daemonless — the
default container engine on Fedora, RHEL, and CoreOS) as well as Docker. The
published images are multi-arch (`linux/amd64` + `linux/arm64`), so Podman pulls
the variant matching your host automatically.

This page covers the few differences from the Docker quickstart in
[setup.md](setup.md). If you are on Docker, ignore this page.

## Prerequisites

- Podman 4.7 or newer (`podman version`)
- A Compose provider, if you want to use the compose files:
  - `podman compose` (Podman's built-in subcommand, shells out to
    `docker-compose` or `podman-compose` if present), **or**
  - `podman-compose` (the standalone Python tool: `pip install podman-compose`)

`podman` itself (build/run) needs no extra packages.

## Quickstart (compose)

The compose files in this repo are Podman-compatible as-is. Use the same
commands as the Docker quickstart, swapping `docker compose` for
`podman compose`:

```bash
cp .env.example .env
# generate and paste the encryption key (see setup.md)
openssl rand -hex 32

podman compose up -d
podman compose ps
```

On startup the container logs a ready banner with the URL to open. Note that
`podman compose` (unlike `docker compose`) sometimes does **not** stream
container stdout to the attach view — a quiet terminal after `Attaching to …`
does not mean it hung. Confirm it's up with either of:

```bash
podman compose logs                          # shows the "Gleipnir … is ready" banner
curl http://localhost:8080/api/v1/health     # -> {"data":{"status":"ok"}}
```

Then open `http://localhost:8080` and complete the setup wizard exactly as in
[setup.md](setup.md#complete-the-setup-wizard).

## Quickstart (no compose)

If you'd rather not install a compose provider, run the image directly. Named
volumes keep your database and installed plugins across restarts:

```bash
podman volume create gleipnir_data
podman volume create gleipnir_plugins

podman run -d --name gleipnir \
  -p 8080:8080 \
  -e GLEIPNIR_ENCRYPTION_KEY="$(openssl rand -hex 32)" \
  -v gleipnir_data:/data \
  -v gleipnir_plugins:/plugins \
  docker.io/felagengineering/gleipnir:latest

# wait for healthy, then:
curl -fsS http://localhost:8080/api/v1/health
```

> Save the encryption key you generate above — losing it makes every stored
> credential permanently unrecoverable. See
> [Operations — Backing up the encryption key](operations.md#backing-up-the-encryption-key).

## Podman-specific notes

### Fully-qualified image names

Podman's default `registries.conf` does **not** silently prepend `docker.io/`
to short image names the way Docker does — an unqualified `felagengineering/...`
either prompts for a registry or fails with `short-name resolution enforced`.
The compose files therefore reference the **fully-qualified**
`docker.io/felagengineering/gleipnir:…`. Use the same qualified path in any
`podman run`/`podman pull` you write by hand.

### SELinux and bind mounts (Fedora / RHEL)

The default compose stack uses **named volumes** (`gleipnir_data`,
`gleipnir_plugins`), which Just Work under SELinux. If you instead **bind-mount**
a host directory — e.g. to drop plugin tarballs from a host folder — add the
`:z` (shared) or `:Z` (private) SELinux relabel suffix, or the container gets
`permission denied`:

```yaml
services:
  api:
    volumes:
      - ./local-plugins:/plugins:z      # :z relabels for container access
```

`:z` is a harmless no-op on non-SELinux systems, so it's safe to leave in.

### Rootless port mapping

Rootless Podman can only bind host ports **≥ 1024**. The default
`GLEIPNIR_PORT=8080` is fine. If you set a privileged port (< 1024) you'll need
`sudo`, rootful Podman, or a `net.ipv4.ip_unprivileged_port_start` sysctl change.

### Reaching host services

To reach a service running on the host (e.g. a local LLM at
`127.0.0.1:1234`), Podman provides the `host.containers.internal` alias
automatically. The build overlay (`docker-compose.build.yml`) also declares
`host.docker.internal` so the same config works under both runtimes.

## CI

The `podman-smoke` job in `.github/workflows/ci.yml` builds the image with
`podman build` and boots it with `podman run` on every PR, so Podman
compatibility is gated and won't silently regress.
