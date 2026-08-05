# Plugin manifest v2 (containerized plugins)

**Status:** part of the MCP realignment (ADR-053/ADR-056, `mcp-realignment-spec.md` §4 and §7).
The v1 gRPC-subprocess manifest (`plugin-sdk/manifest`) remains the live format until
the cutover lands. This document describes `plugin-sdk/manifestv2`, which nothing
loads yet.

## What a manifest is

The manifest is the **consent surface**. It is what an admin reads and approves
before the plugin ever runs: an image, a set of domains it may reach, a resource
ceiling, and the capability profiles that decide which host surfaces light up.

Two consequences follow, and both are deliberate:

- **Parsing is strict.** An unknown field is an error, not something to ignore. A
  field the host silently drops is a claim the admin read and the runtime never
  enforced — which makes the review meaningless.
- **Validation is fail-closed.** A manifest that does not fully parse and validate
  does not install.

## Shape: `server.json` + `gleipnir:`

A v2 manifest is an MCP registry `server.json` record plus Gleipnir's trust fields,
and the split is literal. Base fields use the registry's own vocabulary; everything
Gleipnir adds lives under a single `gleipnir:` key. That keeps "install from a
registry entry + our signature" open as a future distribution path without a
manifest migration.

### Minimal manifest

This is the whole thing for a plain ecosystem MCP server. The profile system's
central claim is that a tool-only server needs no Gleipnir-specific code; this is
what that looks like.

```yaml
schema_version: "2"
name: io.github.example/weather
version: 1.2.0
description: Weather lookups.
package:
  registry_type: oci
  identifier: ghcr.io/example/weather@sha256:0123...  # 64 hex chars
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    tool_provider: {}
```

## Base fields

| Field | Required | Notes |
|-------|----------|-------|
| `schema_version` | yes | Must be `"2"`. A different value is rejected, so an old host refuses a new manifest loudly instead of ignoring the field that would have contained the difference. |
| `name` | yes | Registry vocabulary — reverse-DNS by convention. No whitespace. |
| `version` | yes | The plugin's own version. |
| `description` | no | Shown on the consent screen. |
| `repository.url` / `.source` | no | Informational only; Gleipnir never fetches it. |
| `package.registry_type` | yes | Must be `oci` — a managed plugin runs as a container. |
| `package.identifier` | yes | **Must be digest-pinned.** See below. |
| `package.version` | no | Human-readable version of the artifact, for display. |
| `package.transport.type` | yes | Must be `streamable-http`. |
| `package.transport.port` | no | Container port the server listens on. |

### Why the image must be digest-pinned

A tag is a mutable pointer. An operator consenting to `:v1.2.0` is consenting to
whatever that tag points at tomorrow, which is not consent. The signed bundle
carries the image, so the digest is knowable at authoring time — there is no case
where a tag is the only thing an author could write.

## `gleipnir:` fields

### `profiles` — required, at least one

Profiles (spec §4) declare what the plugin is, which decides which host surfaces
appear. A profile is a key, not a string in a list, because two of them carry
configuration and a list plus a side table would let a plugin configure a profile
it never claimed.

```yaml
gleipnir:
  profiles:
    tool_provider: {}          # baseline: serves MCP tools
    event_source: {}           # implements io.gleipnir/events
    human_channel:
      assurance: authenticated # or: weak — REQUIRED
    identity_provider:
      link_methods: [oauth, code]   # at least one
```

**`human_channel.assurance` is required** and has no default. It states how strongly
the channel authenticates the human acting through it: a platform button click
arrives authenticated, an email `From:` header is forgeable. The **host** — never
the plugin — decides what each level may resolve; default policy lets a weak channel
answer *information* requests while a *permission* request routed there falls
through to the next audience entry. Defaulting this either way would be the host
guessing about somebody else's authentication.

**`identity_provider.link_methods` needs at least one entry.** A provider with no
way to link identities provides no identity.

### `egress` — default deny

A plugin container attaches to an internal-only network and can reach **nothing**
until a grant says otherwise. An empty or absent list is therefore meaningful and
common, not a missing value.

```yaml
  egress:
    - domain: api.example.com
      reason: the vendor API this plugin wraps
    - domain: "*.cdn.example.com"
```

`domain` is a bare hostname. No scheme, no path, no port — those are properties of a
*request*, not of a destination an admin consents to. A single leading `*.` wildcard
covers subdomains; interior wildcards are rejected.

`reason` is optional but strongly encouraged: "why does this plugin need to reach
this host" is the question the reviewing admin is actually asking.

### `resources`

```yaml
  resources:
    memory_mb: 512
    cpu_millicores: 1500   # 1000 == one core
```

Absent means the host default applies. An admin override wins over both.

### `tools` — per-tool review-time facts

This does **not** enumerate the tool list. That comes from `tools/list` at runtime,
and a manifest claiming a tool the server does not serve would be a lie the host
cannot act on. This block is for facts that only matter *before* a tool is called:

```yaml
  tools:
    - name: deploy
      elicitation_kind: permission     # or: information
```

Declaring `elicitation_kind` moves the §6.1 decision to install time, where an admin
sees it, instead of leaving it to the runtime convention (a `requestedSchema` with no
fields is a consent-only ask).

### `event_kinds` — attestation

Declares which event kinds the plugin may emit. Drift between this and the runtime
`events/discover` response is a **health fault**, not a silent merge: the manifest
is what the admin consented to.

```yaml
  event_kinds:
    - kind: message.posted
      description: A message was posted.
      binding_schema:
        type: object
        properties:
          channel: { type: string }
```

### `config_schema` / `user_config_schema`

JSON Schema for per-instance and per-user configuration. The `x-gleipnir-secret`
(ADR-049) and `x-gleipnir-options` annotations carry over from v1 unchanged.

### `sbom`

Optional relative path to a CycloneDX SBOM bundled with the plugin. Surfaced as a
badge; never parsed.

## Canonical form and signing

`Marshal` emits canonical YAML — mapping keys sorted at every level, 2-space indent,
exactly one trailing newline. Sequences keep declaration order, because the order of
tools or egress grants is authored meaning rather than incidental.

Canonical form matters because signing hashes the manifest bytes: the same
declarations must produce byte-identical output, or a re-serialized manifest would
fail its own signature.

## No reserved field names

Per spec §4, a profile contract defines no field names with fixed semantics. A
plugin's binding fields are ordinary vocabulary it declares — `mention_only` is a
boolean a plugin may choose to define, not something the spec knows about. If a
future profile needs a reserved name, that is a signal the profile is wrong.
