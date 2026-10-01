# Drive a Relay fleet (fleet-ops)

**Status:** Complete for both agents on Relay's nine-Node demo fleet, as of 2026-10-01. Relay
asks in-band since [gleipnir-relay#646](https://github.com/Felag-Engineering/gleipnir-relay/issues/646);
`make demo-reset` starts the Relay on its own in-band demo gate (no fixture edit — see
[Step 2](#step-2--relay-side-the-approval-gate-rule-relay-configuration-not-gleipnir)); and the
demo's web Nodes carry a DEV-ONLY `systemctl` shim, so `service.restart` of `nginx` restarts a
real service there. The responder's full path through Gleipnir's own UI — question in the
attention queue, a human answering, the Job succeeding — has not yet been observed end to end;
rehearse it before relying on it.

## What it does

Two agents against one Relay MCP server. The difference between their grants is the point.
fleet-reader answers questions with typed read Operations. fleet-responder turns an Uptime
Kuma DOWN alert into a proposed, scoped `service.restart`. Relay parks that call and asks a
human in Gleipnir's UI over MRTR. The model never sees the question. Gleipnir authenticates as
an ordinary machine Account and gets nothing another harness could not have.

## Prerequisites

- Gleipnir from this repo (main or later, which includes per-server CA trust #928, MRTR
  approval #929 and per-server call timeout #939).
- A Relay with `-control-enabled`. For the demo: the Relay repo's nine-Node demo fleet
  (`make demo-build`, `make demo-reset`; see
  [gleipnir-relay docs/operations/demo-fleet.md](https://github.com/Felag-Engineering/gleipnir-relay/blob/main/docs/operations/demo-fleet.md)).
- For the responder's approval step: a Relay build that includes
  [gleipnir-relay#646](https://github.com/Felag-Engineering/gleipnir-relay/issues/646).
- An LLM provider key at Admin → Models.
- Docker Compose >= 2.24 if you use the overlay.

## The grant table

| Tool | fleet-reader | fleet-responder | Why |
|---|---|---|---|
| `list_nodes`, `describe_node`, `list_operations`, `get_job` | yes | yes | read-only |
| `run_operation` | yes | yes (`params`: selector/operation/args/plan) | typed Operations only |
| `raw_exec` | never | never | see below |
| `approve_request` | never | never | see below |
| `cancel_job` | no | no | not needed (a machine Account may cancel only its own Jobs) |

No Relay tool carries Gleipnir's `approval: required` in this pack.

## What is not granted, and why (operator note, stated as design)

- **`raw_exec`:** a capability the agent was never given is never registered. No phrasing can
  reach it (ADR-001). Leaving it out is a stronger control than gating it, so keep the gate for
  things the agent actually needs. (In the demo fleet it is also refused by role, since Raw
  Exec needs `admin`, and parked by Relay's gate. Neither of those is why it is ungranted.)
- **`approve_request`:** its `approver` argument is free text the caller types. Under the gate
  rule [Step 2](#step-2--relay-side-the-approval-gate-rule-relay-configuration-not-gleipnir)
  requires (in-band, `requester_allowed` true), Relay would accept the model's own
  `approve_request` with any name typed in. That makes it a self-approval button. Gleipnir's
  MRTR answer differs because Gleipnir fills the approver identity from the authenticated
  session and the model never sees the question. The only thing keeping the model away from
  `approve_request` is that it is not granted.
- **Recommended belt-and-braces:** disable `raw_exec` and `approve_request` on the Tools page.
  Disabled tools cannot be granted to any agent, so a future policy cannot add them by
  accident.

## What keeps fleet-reader read-only (honest)

Nothing in Gleipnir does. `run_operation` is one tool for both risk classes. `params` cannot
restrict values, and Relay's `operator` role (the lowest that can run a read Operation) can
also request a mutate. The prompt tells it not to, and that is guidance, not a control. The
structural bounds are Relay's: its approval gate, the mutate blast cap, and each Node's Policy.
On the demo fleet's gate a mutate parks for a human only at fan-out 2 or more; **a fan-out-1
mutate runs with no human** (on the demo roster that means the db or bastion Node alone, where
it fails or is refused). On your own Relay, the account-scoped rule in
[Step 2](#step-2--relay-side-the-approval-gate-rule-relay-configuration-not-gleipnir) parks every
mutate by Gleipnir's Account at any fan-out. If you need read-only to be structural,
register a second server with a `viewer` token. The cost: `viewer` cannot call `run_operation`
at all, so the reader loses fleet-wide reads.

## Step 1 — Bring up the Relay fleet

Relay repo root: `make demo-build`, `make demo-reset`. Map the SAN once:

```bash
grep -q 'relay$' /etc/hosts || echo '127.0.0.1 relay' | sudo tee -a /etc/hosts
```

Your own Relay: start it with `-control-enabled` and
`-hostname <dns-name-gleipnir-will-dial>`.

## Step 2 — Relay-side: the approval gate rule (Relay configuration, not Gleipnir)

A gate file is root-owned Relay config (`RELAY_APPROVAL_GATE_CONFIG` / `-approval-gate-config`),
loaded only at startup. Matching rules merge to the strictest requirement. Relay asks the
question in-band only of a client that declares the name `gleipnir` (Gleipnir does), and only
under an **in-band** rule; under an out-of-band rule Gleipnir sees a bare `pending_approval`.

**Demo fleet: nothing to do.** `make demo-reset` and `make demo-up` start the Relay on
`docker/fixtures/approval-gates.demo.json`, whose two rules are in-band with
`requester_allowed: true` and no audience: `demo-fleet-wide-mutates-need-a-human` (mutate,
fan-out ≥ 2) and `demo-raw-exec-always-needs-a-human`. Use the `make demo-*` targets — a bare
`docker compose ... up` gets the default fleet's out-of-band gate instead. Relay records an
in-band answer as decided by Gleipnir's verified machine Account, with the Gleipnir user who
answered as an asserted name beside it.

**Your own Relay:** the rule that matches Gleipnir's mutates must be in-band and
`requester_allowed: true`. If it names an audience, the audience must contain Gleipnir's
machine Account (the Account that delivers the answer). A stricter variant that parks every
mutate by that Account at any fan-out:

```json
{
  "rules": [
    {
      "name": "gleipnir-mutates-ask-a-gleipnir-approver",
      "match": { "account": "auto-incident-response", "risk_class": "mutate" },
      "require": { "count": 1, "audience": ["auto-incident-response"], "requester_allowed": true,
                   "channel": "in-band", "ttl": "1h" }
    },
    {
      "name": "raw-exec-always-needs-a-human",
      "match": { "raw_exec": true },
      "require": { "count": 1, "audience": ["dev-approver"], "channel": "out-of-band", "ttl": "1h" }
    }
  ]
}
```

`match` has no negation. A broad out-of-band mutate rule (like the default fleet's
`dev-fleet-wide-mutates-need-a-human`) would ALSO match Gleipnir's dispatches and win the
merge, so scope such rules to other Accounts with `account`. Replace `auto-incident-response`
with whatever you named Gleipnir's Account in Step 3.

## Step 3 — Mint Gleipnir's machine Account and API token

Demo fleet: run Relay's "Credentials for Gleipnir (copy-paste)" block in
`docs/operations/demo-fleet.md` as written. Its effect: Account `auto-incident-response`, kind
machine, role `operator`, token ttl 86400, written to
`.dev-fleet/auto-incident-response-credential`. Re-run it after every `make demo-reset`, which
destroys the store. (Relay's runbook recommends one Account per agent; with one `relay` server
registered in Gleipnir, both agents share this one, and Relay tells them apart by the
on-behalf-of attribution in Step 7.) Own Relay: an admin creates a machine Account at
`operator` (the lowest role that can run a read Operation) and mints a token with an explicit
TTL. The token is shown once.

## Step 4 — Get the CA certificate

The file is `<ca-dir>/ca-cert.pem` (demo: `.dev-fleet/ca-cert.pem`). Never `ca-key.pem`.
Optional fingerprint check:

```bash
openssl x509 -in .dev-fleet/ca-cert.pem -noout -fingerprint -sha256
```

Compare it with the SHA-256 shown in Gleipnir's CA section, which uses the same hex format as
Relay's logged `ca_fingerprint`.

## Step 5 — Start Gleipnir so it can reach Relay

```bash
docker compose -f docker-compose.yml -f docs/playbooks/fleet-ops/docker-compose.relay.yml up -d
```

The overlay is [`docker-compose.relay.yml`](docker-compose.relay.yml). The SAN rule: the URL
host must equal Relay's `-hostname` (demo: `relay`). An IP or `localhost` fails verification
with a hostname mismatch, and verification is never disabled. Non-container alternative: add
`127.0.0.1 relay` to `/etc/hosts` (the same line Relay's `dev-fleet.md` uses), then start
Gleipnir normally.

## Step 6 — Gleipnir users

First-run admin setup. Provider key at Admin → Models. At Users (`/admin/users`), create a user
with the `approver` role. That person answers Relay's question. The `operator` role cannot
answer permission asks. Admins can, but use a separate approver so the person who sets up the
policy is not the one who approves.

## Step 7 — Register Relay as an MCP server

Tools → Add MCP server:

- Name: `relay` (exactly; the YAML references `relay.<tool>`)
- URL: `https://relay:9443/mcp`
- CA certificate: paste `.dev-fleet/ca-cert.pem`
- Call timeout: `120` seconds (Relay runs an approved Job synchronously inside the retried
  `tools/call`; the 30s default is too short for a fan-out)
- Run attribution: `Relay preset` (sends `X-Relay-On-Behalf-Of`, `X-Relay-Session-Ref`,
  `traceparent` on every tool call, so Relay's attribution screen shows which agent and run
  asked)
- Auth header: `Authorization` = `Bearer <contents of .dev-fleet/auto-incident-response-credential>`

Expected: badge "Protocol 2026-07-28", eight tools, Call timeout `120s`, and Run attribution
`Relay preset` in the server's detail. If the badge reads "Legacy protocol" or "Protocol
unknown", press Rediscover. A legacy
pin means Relay's approval question never reaches a human
([gleipnir-relay#646](https://github.com/Felag-Engineering/gleipnir-relay/issues/646)). Then
disable `raw_exec` and `approve_request` (recommended). Do this BEFORE Step 8, because `params`
are validated against the discovered schema only at save time.

## Step 8 — Create the two agents

Preferred, because it is exact: POST the files.

```bash
curl -c jar -H 'Content-Type: application/json' \
  -d '{"username":"<admin>","password":"<pw>"}' \
  http://localhost:8080/api/v1/auth/login

curl -b jar -H 'Content-Type: application/yaml' \
  --data-binary @docs/playbooks/fleet-ops/fleet-reader.yaml \
  http://localhost:8080/api/v1/policies

curl -b jar -H 'Content-Type: application/yaml' \
  --data-binary @docs/playbooks/fleet-ops/fleet-responder.yaml \
  http://localhost:8080/api/v1/policies
```

Check that `data.warnings` in each response is `[]`. A `params` warning means the server was
not discovered first. The UI does not show warnings.

Alternative: build them in Agents → New Agent. The form cannot author `params` or
`max_elicitations_per_run`, but it keeps both on later saves.

If your model/provider differs, edit `model:` first.

Then the webhook secret: open fleet-responder → Trigger → Generate initial secret. Its response
is the same envelope every `rotate` call returns:

```json
{"data": {"secret": "..."}}
```

(or `POST /api/v1/policies/{id}/webhook/rotate`, whose response is the same shape).

## Step 9 — Ask the fleet a question (reader acceptance)

Agents → fleet-reader → Run now, with a message. Questions that work on the demo fleet:
"Which Nodes run which Policy fingerprint, and which of them may restart nginx?"; after
`make demo-fault`, "Which web Nodes are not serving on 127.0.0.1:8080?" (`file.read`
`/proc/net/tcp`). Expected trace: `capability_snapshot` with exactly the five `relay.*` tools,
then `list_nodes`, then `run_operation` fanned out, then a summary. One run. On Relay's own
attribution screen, this shows up on-behalf-of `fleet-reader (triggered by <you>)` — the
manual trigger asserts the operator's username alongside the agent name.

## Step 10 — Fire the responder (responder acceptance)

Point to [devops/README.md Step 6](../devops/README.md#step-6--connect-uptime-kuma) for the
Uptime Kuma notification setup (bearer, JSON preset). Do not duplicate it. For the demo fleet,
whose service is not reachable from Uptime Kuma, send a Kuma-shaped payload:

```bash
curl -X POST http://localhost:8080/api/v1/webhooks/<policy-id> \
  -H "Authorization: Bearer <policy-secret>" -H "Content-Type: application/json" \
  -d '{"heartbeat":{"status":0,"msg":"nginx not answering on dev-node-4, dev-node-5","important":true},
       "monitor":{"name":"nginx","url":"http://dev-node-4:8080","type":"http"},
       "msg":"[nginx] [Down] nginx not answering on dev-node-4, dev-node-5"}'
```

Expected: `202` with a `run_id`. The trace shows `list_nodes` → `file.read` of `/proc/net/tcp`
on the affected Nodes → usually a `plan: true` preview (Relay's Policy verdicts; it executes
nothing and never parks) → `run_operation` `service.restart` `{unit: nginx}`, selector
`node:role=web, node:env=prod` (fan-out 2) → the same read again to verify. The prompt takes
the port from `monitor.url`; with no port there, it compares the faulted Node against its
healthy sibling. If every named Node is already listening, the agent restarts nothing. The run
moves to `waiting_for_feedback`, and a "TOOL ASK" row attributed to `relay` appears in the
attention queue. The approver approves on the run page, and Relay runs the Job in the same
(retried) call. Relay's Job shows on-behalf-of `fleet-responder` and session ref
`gleipnir run <run_id>` (plus the run's URL when `public_url` is set) — the asserted join back
to this Gleipnir run. On the demo fleet `dev-node-4` and `dev-node-5` then report
`success, exit_code: 0` *(predicted on this Selector)*: their `systemctl` is a DEV-ONLY shim that
restarts the real demo service. Fault only `daemon-4` first if you want the restart to fix
something (`make demo-fault DEMO_FAULT_NODES="daemon-4"` in the Relay repo; the default also
downs `daemon-6`, in staging, which this alert does not name). Resend with `status: 1` and
expect `200 {"data":{"filtered":true}}`. Without
[gleipnir-relay#646](https://github.com/Felag-Engineering/gleipnir-relay/issues/646), or with an
out-of-band rule, the agent gets a `pending_approval` result and reports it. Nothing dispatches.

## Parameter scoping on run_operation — what it does and does not do

- Relay's `run_operation` schema is a flat object: `selector`, `operation` (required), `args`,
  `timeout_seconds` (0..86400), `plan`. Nothing sits under `oneOf`/`anyOf`/`$ref` at the root, so
  Gleipnir's narrowing applies. The agent is SHOWN only `selector`/`operation`/`args`/`plan`,
  and dispatch refuses any other key (the allowlist comes from the `params` block itself,
  #769). The save returns no warning. If Relay ever moves to a branching schema, the save will
  warn, and narrowing will then stop reaching the branches.
- What it cannot do: `params` values are ignored. It cannot pin the Operation vocabulary,
  cannot restrict risk class, and cannot bound the selector. Dropping `selector` would make
  every call fleet-wide. Dropping `operation` would make every call fail.
- So this is defense in depth with a small reach, not the primary control. The controls on
  WHICH Operation hits WHICH Nodes are Relay's gate and blast cap and each Node's Policy, and
  none of them can be reached from Gleipnir.

## Double approval in production

Adding `approval: required` to `relay.run_operation` is supported and tested. Gleipnir's
prompt comes first, then Relay's, both in the same UI. Two costs: it gates reads and `plan:
true` previews too, because `run_operation` is one tool for both risk classes. And it is two
approvals per mutate. This pack leaves approval to Relay, which gates per risk class and binds
the approval to a Relay-authored plan hash.

## After `make demo-reset`

The CA and the token are both new. Re-run Step 3 (mint the token), then, from the Gleipnir repo
root:

```bash
export RELAY_DIR=<gleipnir-relay checkout> GLEIPNIR_ADMIN_USER=<admin>
read -rs GLEIPNIR_ADMIN_PASSWORD && export GLEIPNIR_ADMIN_PASSWORD
scripts/demo-fleet-ops.sh sync
```

`sync` replaces the CA and the `Authorization` header on `relay` from the Relay repo's
`.dev-fleet/`, rediscovers, and fails unless the protocol pin is `2026-07-28`, the call
timeout `120`, run attribution `relay`, the pinned CA's SHA-256 equals the live file's, and
`raw_exec`/`approve_request` are still disabled. `scripts/demo-fleet-ops.sh check` is the
read-only pre-flight afterwards (one PASS/FAIL line per item). The same script's `setup` does
Steps 6–8 and the webhook secret in one idempotent run, apart from the provider key; see the
investor demo runbook ([docs/demo/investor-demo.md](../../demo/investor-demo.md), §2) for the
environment it reads. It never takes a password or token on its command line.

By hand, if the script cannot run: in Tools → relay, replace the CA (server detail → CA
certificate) and the Authorization header value, then press Rediscover.

Either way no Gleipnir restart is needed, because the PEM and headers are part of the client
cache key. Policies, the protocol pin, and the Run attribution setting (it is on the Gleipnir
row, not the Relay side) all survive.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| TLS "hostname mismatch" | URL host ≠ Relay `-hostname`. |
| "unknown authority" / wrong pinned CA | Stale CA after a reset. |
| `401` | Token expired (24h) or wiped by a reset. |
| Relay role error on `run_operation` | The Account is a `viewer`. |
| Badge Legacy / unknown | Rediscover. |
| Agent reports `pending_approval` | The gate rule is out-of-band (demo fleet started with a bare `docker compose up`, not `make demo-reset`), or the Relay lacks #646. |
| Approval answered but refused (not in audience / requester excluded) | The gate rule lacks audience `gleipnir` or `requester_allowed` true. |
| Tool call times out after approval | The `relay` server's Call timeout is not `120s` (detail modal shows `Default (30s)`). Set it there; no restart needed. |
| Relay shows no on-behalf-of / session ref | Run attribution is `Off` on the `relay` server's detail. Set it to `Relay preset`; no restart needed. |
| Run fails with feedback timeout | Nobody with the `approver` role answered within `feedback.timeout`. |
| `spawn systemctl` failure | A `service.restart` reached a db Node (no shim there — the demo fleet's busybox limit). Web Nodes have the shim. |
| `data.warnings` about `params` | Server not discovered before the POST. |
| Webhook `401` / `403` / `409` / `filtered` | As in devops. |
