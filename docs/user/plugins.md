# Plugins

Plugins extend Gleipnir with tools, notification channels, and trigger event sources. Each plugin runs as a sandboxed subprocess; Gleipnir enforces the same hard capability boundary for plugin tools as it does for MCP tools. This page covers how to install, sign, approve, and manage the key lifecycle for a plugin.

## Installing a plugin

Plugins are distributed as `.tar.gz` (or `.tgz`) tarballs. There are two ways to install one.

### Filesystem drop

Drop the tarball into the directory set by `GLEIPNIR_PLUGINS_DIR` (default: `/plugins`). Gleipnir's fsnotify watcher detects new files within roughly 250 ms and installs the bundle automatically. If the server was down when a tarball arrived, the restart sweep picks it up on next startup. When multiple tarballs for the same plugin name are present, only the one with the highest semver is installed.

Using Docker Compose with the default volume:

```bash
docker cp my-plugin-1.2.0.tar.gz gleipnir-api:/plugins/
```

### Admin API upload

Send the raw tarball as an HTTP request body to the install endpoint (admin role required):

```bash
curl -X POST https://<host>/api/v1/admin/plugins \
  -H "Content-Type: application/octet-stream" \
  --data-binary @my-plugin-1.2.0.tar.gz
```

Maximum body size: 100 MiB. On success the response is `{ "data": { "id": "...", "name": "...", "version": "...", "status": "..." } }`. The `status` field reflects the post-install state, which may be `pending_review` for signed plugins awaiting admin approval (see below).

Both paths run the same extract → signature-verify → snapshot install pipeline.

## Approving a plugin (pending\_review)

Signed plugins land in `pending_review` after installation. They are present in the database but no subprocess is spawned until an admin approves them. Unsigned plugins that loaded under `GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true` auto-activate and skip this step.

**Review surface:**

- `GET /api/v1/admin/plugins` — lists all plugins including those in `pending_review` (which have no instances yet).
- `GET /api/v1/admin/plugins/{id}` — full plugin detail: manifest metadata, services declared, tier-2 capabilities, auth strategy, pubkey fingerprint, SBOM presence.

In the UI, go to **Admin → Plugins** and open the plugin's review page at `/admin/plugins/:id/review`.

**Approve:**

```bash
curl -X POST https://<host>/api/v1/admin/plugins/{id}/approve
```

Transitions status `pending_review → active`, emits a `plugin_review_approved` audit event, and unblocks instance spawning. Returns 409 if the plugin is not in `pending_review`.

**Reject:**

```bash
curl -X POST https://<host>/api/v1/admin/plugins/{id}/reject
```

Deletes the database row and the bundle directory. The tarball can be re-dropped to re-install. Returns 409 if the plugin is not in `pending_review`.

## Making an instance functional

Each installed plugin can have one or more named instances. Instances are the running subprocesses; a plugin row without instances does nothing at runtime.

### Create an instance

```bash
curl -X POST https://<host>/api/v1/admin/plugins/{id}/instances \
  -H "Content-Type: application/json" \
  -d '{"instance_name": "my-instance"}'
```

Status codes:
- `201` — instance created with health `unhealthy` / detail `config_missing`.
- `400` — `instance_name` is missing or whitespace-only.
- `409` — `instance_name` already exists for this plugin.

A freshly-created instance is immediately visible in the admin UI but shows health `unhealthy` with detail `config_missing`. This is expected — the subprocess does not start until the instance is configured. Once you set its config (and credentials, if required), the health state clears.

### Configure the instance

After creation, the instance needs to be configured before it becomes healthy. The exact steps depend on what the plugin's manifest declares.

**Instance config** (non-secret fields):

```bash
curl -X PUT https://<host>/api/v1/admin/plugins/{id}/instances/{iid}/config \
  -H "Content-Type: application/json" \
  -d '{"config": {"option_key": "value"}, "expected_version": 0}'
```

**Secret config fields** (write-only; avoids round-trip clobber):

```bash
curl -X PUT https://<host>/api/v1/admin/plugins/{id}/instances/{iid}/config/{property} \
  -H "Content-Type: application/json" \
  -d '{"value": "secret-value", "expected_version": 1}'
```

GET responses for config redact secret fields (annotated `x-gleipnir-secret: true` in the manifest) to `"***"`.

**Subscription scope** (trigger plugins; restarts the trigger stream immediately):

```bash
curl -X PUT https://<host>/api/v1/admin/plugins/{id}/instances/{iid}/subscription-scope \
  -H "Content-Type: application/json" \
  -d '{"scope": {...}, "expected_version": 0}'
```

**Credentials:**

| Strategy | Endpoint | Body |
|---|---|---|
| Static API key | `PUT .../credentials/static-api-key` | `{"header_name": "X-Api-Key", "scheme": "Bearer", "api_key": "..."}` |
| Individual headers | `PUT .../credentials/headers/{name}` | `{"value": "..."}` |
| Basic auth | `PUT .../credentials/basic-auth` | `{"username": "...", "password": "..."}` |
| OAuth2 — step 1: store client creds | `PUT .../credentials/oauth-client` | `{"client_id": "...", "client_secret": "..."}` |
| OAuth2 — step 2: start flow | `POST .../oauth/begin` | starts the OAuth2 flow |
| Direct OAuth token seed | `PUT .../credentials/oauth-token` | `{"access_token": "...", "refresh_token": "...", "expires_at": "2026-06-19T00:00:00Z"}` |

For `oauth2_authcode` and `oauth2_clientcred` strategies, you must call `PUT .../credentials/oauth-client` first — `BeginAuthcode` reads the client ID and secret from the stored credentials and fails with a config error if they are absent. Only after storing the client credentials should you call `POST .../oauth/begin` to start the authorization flow.

All `PUT .../credentials/*` calls reject `"***"` as a value. Header names are validated against the RFC 7230 header-name rules and the reserved-name blocklist.

### Deactivate and reactivate

```bash
# Soft-stop subprocess, transition health to inactive
curl -X POST https://<host>/api/v1/admin/plugins/{id}/instances/{iid}/deactivate

# Restart subprocess from inactive
curl -X POST https://<host>/api/v1/admin/plugins/{id}/instances/{iid}/activate
```

## Signing a plugin with the gleipnir-plugin CLI

Install the CLI from the plugin SDK, then use the `keygen`, `sign`, and `package` subcommands to produce a signed bundle. Signed bundles are verified by the host on every install and hot-reload.

### Generate a signing keypair

```bash
gleipnir-plugin keygen \
  --out-dir ~/.config/gleipnir-plugin/keys \
  --name signing \
  --kdf scrypt
```

This writes `~/.config/gleipnir-plugin/keys/signing.key` (mode 0600, passphrase-encrypted) and `signing.pub` (mode 0644).

**Key flags:**

| Flag | Default | Description |
|---|---|---|
| `--out-dir` | `~/.config/gleipnir-plugin/keys` | Directory for key files |
| `--name` | `signing` | Base name → `<name>.key` / `<name>.pub` |
| `--kdf` | `scrypt` | KDF for passphrase encryption: `scrypt` (broadest compatibility) or `argon2` (requires minisign ≥ 0.11) |
| `--force` | false | Overwrite existing key files |
| `--passphrase-stdin` | false | Read passphrase from stdin (CI) |
| `--unencrypted` | false | Skip passphrase encryption — **testing only** |

In CI, set `GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE` to avoid interactive prompts.

**Back up `signing.pub` separately from `signing.key`.** The public key is what gets TOFU-pinned on the host at first install; you need it to perform key rotation (see below).

### Sign a binary and manifest

`sign` produces a standalone `.minisig` file. Use it when you want to sign independently of packaging.

```bash
gleipnir-plugin sign \
  --binary ./my-plugin \
  --manifest manifest.yaml
```

Output: `my-plugin.minisig` in the current directory.

**Key flags:**

| Flag | Default | Description |
|---|---|---|
| `--binary` | *(required)* | Path to the plugin binary |
| `--manifest` | `manifest.yaml` | Path to `manifest.yaml` |
| `--key` | `~/.config/gleipnir-plugin/keys/signing.key` | Path to `.key` file |
| `--key-stdin` | false | Read `.key` file from stdin (CI) |
| `--out` | `<binary-basename>.minisig` | Output `.minisig` path |
| `--trusted-comment` | *(auto)* | Trusted comment embedded in the signature (default: timestamp + manifest name/version) |

Key resolution order: `--key-stdin` → env `GLEIPNIR_PLUGIN_SIGNING_KEY` → `--key` → default path.

### Package a signed release bundle

`package` builds the complete `.tar.gz` ready for installation:

```bash
gleipnir-plugin package \
  --binary ./my-plugin \
  --manifest manifest.yaml \
  --out-dir dist
```

Output: `dist/my-plugin-1.2.0.tar.gz` containing:
```
my-plugin-1.2.0/
  my-plugin           (mode 0755)
  manifest.yaml       (mode 0644)
  my-plugin.minisig   (mode 0644)  ← filename is <manifest.name>.minisig, not the binary basename
  signing.pub         (mode 0644)
```

An optional SBOM is included when you pass `--sbom`:

```bash
gleipnir-plugin package \
  --binary ./my-plugin \
  --manifest manifest.yaml \
  --sbom sbom.cyclonedx.json \
  --out-dir dist
```

**Key flags:**

| Flag | Default | Description |
|---|---|---|
| `--binary` | *(required)* | Path to the plugin binary |
| `--manifest` | `manifest.yaml` | Path to `manifest.yaml` |
| `--key` | `~/.config/gleipnir-plugin/keys/signing.key` | Path to `.key` file |
| `--key-stdin` | false | Read `.key` from stdin (CI) |
| `--pubkey` | sibling `.pub` of the `.key` | Path to `.pub` file |
| `--out-dir` | `dist` | Output directory for the tarball |
| `--sbom` | *(none)* | Optional CycloneDX SBOM JSON path |
| `--unsigned` | false | Produce an unsigned bundle — only loads on hosts with `GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true` |

Key resolution order for `package`: same as `sign` (`--key-stdin` → env `GLEIPNIR_PLUGIN_SIGNING_KEY` → `--key` → default path).

### CI signing example

```bash
# Key and passphrase available as CI secrets:
export GLEIPNIR_PLUGIN_SIGNING_KEY=/path/to/signing.key
export GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE=<secret>

gleipnir-plugin package \
  --binary ./dist/my-plugin \
  --manifest manifest.yaml \
  --out-dir release
```

## TOFU key trust and rotation

Gleipnir uses trust-on-first-use (TOFU) pinning for plugin signing keys.

### First install pins the key

When a signed plugin is installed for the first time, its public key is pinned in the database (`plugins.trusted_pubkey`). Every subsequent install or hot-reload of that plugin must be signed by the same key. This is the tamper-evidence guarantee.

### Update signed by a different key

If you drop in a bundle signed by a key that does not match the pinned key, Gleipnir does not auto-install it. Affected instances transition to `pending_key_approval` and a high-severity `plugin_pubkey_mismatch` audit event is emitted. The audit event payload includes the field `new_pubkey_b64` — the base64-encoded Minisign `signing.pub` bytes of the new key.

To approve the new key, post that value to the accept-new-key endpoint:

```bash
# Extract new_pubkey_b64 from the audit log, then:
curl -X POST https://<host>/api/v1/admin/plugins/{id}/accept-new-key \
  -H "Content-Type: application/json" \
  -d '{"candidate_pubkey": "<value of new_pubkey_b64 from audit event>"}'
```

This CAS-rotates `plugins.trusted_pubkey`, unblocks the `pending_key_approval` instances back to healthy, and emits a `plugin_pubkey_rotated` audit event. In the admin UI, the **AcceptNewKeyModal** extracts `new_pubkey_b64` automatically — you only need the manual API path when scripting.

### Material manifest changes on hot-reload

When a plugin update changes the manifest in a material way (schema shape changes, not just description/default edits), the hot-reload is blocked: affected instances transition to `pending_manifest_approval` and a high-severity `plugin_manifest_material_change` audit event is emitted.

To commit the new manifest:

```bash
curl -X POST https://<host>/api/v1/admin/plugins/{id}/accept-manifest
```

Depending on whether the new manifest introduces newly-required config fields, instances transition to `healthy` (no new required fields) or `pending_config_migration` (new required fields that need filling). The admin UI provides a review surface at **Admin → Plugins → {plugin}**.

## Unsigned plugin escape hatch

`GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=false` is the default and should stay that way in any shared or production deployment. Setting it to `true` is a development convenience that disables tamper-evidence for unsigned bundles globally — **signed bundles are still fully verified even in permissive mode**.

When `GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true`:

- The server emits a loud `WARN` log line at startup:
  ```
  GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true: unsigned plugins will load; every load emits a high-severity audit event. Signed plugins are still fully verified. See ADR-045.
  ```
- `GET /api/v1/health` includes an additional field: `"signature_verification": "disabled"`.
- Every unsigned plugin load emits a high-severity audit event.
- Each instance spawned from an unsigned bundle shows an **`unsigned_permissive`** (yellow) health chip labelled **"Unsigned (permissive)"** on its row in **Admin → Plugins**. This is a per-instance indicator — there is no persistent global banner.

This flag is read once at startup. To change it, update the value in `.env` and restart the stack.
