# ntfy

A notify-only `io.gleipnir/channel` plugin that publishes Gleipnir
notifications to an [ntfy](https://ntfy.sh) topic. It is a v2 plugin: a signed,
containerized MCP server on streamable HTTP. It has no gRPC or go-plugin
involvement and links nothing from `plugin-sdk/serve`.

## What it demonstrates

- The smallest useful channel: `mcpserver` + `channelext` with a notify handler
  and no `channelext.WithRequest`, so `channel/request` is answered with
  method-not-found.
- A `weak` assurance declaration. An ntfy topic is a shared space and cannot
  establish which Gleipnir user acted; the manifest and the served
  `server/discover` declaration both say so, and a test pins them together.
- Instance configuration through the host endpoint (`host/get_instance_config`),
  including an `x-gleipnir-secret` field, and logging through `host/log`.

## Behaviour

`channel/notify` POSTs the message body to `<server_url>/<topic>`.

- The topic is the target's `address`, falling back to `default_topic`. It must
  match ntfy's own topic syntax (1-64 letters, digits, `-`, `_`); anything else
  is refused before a request is made, so an address cannot add path segments
  or a query string.
- A non-2xx response, a transport failure, or an invalid config fails the call
  with a JSON-RPC error. The caller's context bounds the publish.
- `Authorization: Bearer <api_token>` is sent only when a token is configured.
- Config is read on every notification, so an admin's change applies without a
  container restart.

## Instance config

| Field           | Default           | Description |
|-----------------|-------------------|-------------|
| `server_url`    | `https://ntfy.sh` | Base URL of the ntfy server |
| `default_topic` | none              | Topic used when a notification's address is empty |
| `api_token`     | none              | Access token for protected topics (secret, read back as `***`) |

## Egress

The manifest declares `ntfy.sh` only. A self-hosted `server_url` needs an
egress grant for its host; letting operators add destinations at configuration
time is a post-alpha contract adjustment.

## Layout

```
main.go          server composition, LISTEN_ADDR (default :8080), graceful shutdown
notify.go        the channel/notify handler
manifest.yaml    the v2 manifest (placeholder image digest; the Makefile stamps a copy)
Dockerfile       static, distroless, nonroot image; build from the repository root
Makefile         image, archive, manifest-digest, bundle
notify_test.go   behaviour tests and manifest-vs-served-surface pins
```

## Test

```sh
make test        # go vet + go test -race
```

## Build and sign a bundle

Needs a container engine (`CONTAINER_ENGINE=podman` for Podman) and a signing
key kept outside the repository (`gleipnir-plugin keygen`, with the `.pub` file
next to the key):

```sh
make bundle SIGNING_KEY=/path/to/ntfy.key
```

The chain is `image` -> `archive` (`docker save`) -> `manifest-digest` (stamps
the archive's image config digest into a copy of `manifest.yaml` under
`dist/`, leaving the committed file untouched) -> `bundle` (signs and packages
into `dist/bundle`). Install the result through the admin UI at
`/admin/plugins`.
