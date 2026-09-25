# Drive a Relay fleet (fleet-ops)

**Status:** Complete for the reader. The responder's in-band approval needs all three of:
a Relay build with [gleipnir-relay#646](https://github.com/Felag-Engineering/gleipnir-relay/issues/646)
(MRTR emits an answerable in-band question at all), the approval-gate rule in
[Step 2](#step-2--relay-side-the-approval-gate-rule-relay-configuration-not-gleipnir) (the
shipped demo fixture is out-of-band and will not satisfy this), and a working restart on the
demo Nodes — either
[gleipnir-relay#626](https://github.com/Felag-Engineering/gleipnir-relay/issues/626) (so a
Template Operation can be dispatched by name) or an equivalent fix to the busybox image, since
today the native `service.restart` always fails there
(see [Step 10](#step-10--fire-the-responder-responder-acceptance)). The reader works today.

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
structural bounds are Relay's: the [Step 2](#step-2--relay-side-the-approval-gate-rule-relay-configuration-not-gleipnir)
gate rule parks every mutate by the `gleipnir` Account (any fan-out) until a human in Gleipnir
answers, plus the mutate blast cap and each Node's Policy. With Relay's SHIPPED demo fixture
instead, a fan-out-1 mutate runs with no human. If you need read-only to be structural,
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
loaded only at startup. Matching rules merge to the strictest requirement. The shipped demo
fixture (`docker/fixtures/approval-gates.json`) is out-of-band, so
[gleipnir-relay#646](https://github.com/Felag-Engineering/gleipnir-relay/issues/646) will not
emit an answerable MRTR question against it and Gleipnir will only ever see a bare
`pending_approval` result. For in-band approval from Gleipnir, the rule matching the `gleipnir`
Account's mutates must be in-band, `requester_allowed` true, and have an audience containing
`gleipnir`. The audience is checked against the Account that delivers the answer (Gleipnir's
machine Account). The human is recorded as the asserted approver.

**How the gate file actually reaches the demo fleet today:** it is `COPY`'d into the Relay
image at build time (`docker/relay.Dockerfile:40`), and there is no compose-level override to
mount a different one. So applying the rule below means editing
`docker/fixtures/approval-gates.json` in a Relay checkout, then `make demo-build` followed by
`make demo-reset` to rebuild and restart the fleet on it. This gap — a way to mount a demo-only
gate file instead of editing the shipped fixture — is requested from the Relay side in
[gleipnir-relay#451](https://github.com/Felag-Engineering/gleipnir-relay/issues/451)
([comment](https://github.com/Felag-Engineering/gleipnir-relay/issues/451#issuecomment-5805986410)).

```json
{
  "rules": [
    {
      "name": "gleipnir-mutates-ask-a-gleipnir-approver",
      "match": { "account": "gleipnir", "risk_class": "mutate" },
      "require": { "count": 1, "audience": ["gleipnir"], "requester_allowed": true,
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

`match` has no negation. A broad out-of-band mutate rule (like the fixture's
`dev-fleet-wide-mutates-need-a-human`) would ALSO match Gleipnir's dispatches and win the
merge, so scope such rules to other Accounts with `account`. This rule is per Relay's current
`approval.Decide`. Re-check it when
[gleipnir-relay#646](https://github.com/Felag-Engineering/gleipnir-relay/issues/646) lands.

## Step 3 — Mint Gleipnir's machine Account and API token

Demo fleet: follow Relay's "Credentials for Gleipnir (copy-paste)" block in
`docs/operations/demo-fleet.md`. Its effect: Account `gleipnir`, kind machine, role `operator`,
token ttl 86400, written to `.dev-fleet/gleipnir-credential`. Own Relay: an admin creates a
machine Account at `operator` (the lowest role that can run a read Operation) and mints a token
with an explicit TTL. The token is shown once, and a demo reset destroys it.

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
- Auth header: `Authorization` = `Bearer <contents of .dev-fleet/gleipnir-credential>`

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
       "monitor":{"name":"nginx","url":"http://dev-node-4","type":"http"},
       "msg":"[nginx] [Down] nginx not answering on dev-node-4, dev-node-5"}'
```

Expected: `202` with a `run_id`. The trace shows `list_nodes` → a read → `run_operation`
`service.restart` `{unit: nginx}`, selector `node:role=web, node:env=prod` (fan-out 2). The run
moves to `waiting_for_feedback`, and a "TOOL ASK" row attributed to `relay` appears in the
attention queue. The approver approves on the run page, and Relay runs the Job in the same
(retried) call. Relay's Job shows on-behalf-of `fleet-responder` and session ref
`gleipnir run <run_id>` (plus the run's URL when `public_url` is set) — the asserted join back
to this Gleipnir run. On the demo fleet each web Node then reports `failure: spawn systemctl: No such
file or directory`. That is the busybox limit
([gleipnir-relay#626](https://github.com/Felag-Engineering/gleipnir-relay/issues/626), or an
equivalent fix to the demo image), not a Gleipnir fault; say so. Resend with `status: 1` and
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

The CA and the token are both new. In Tools → relay: replace the CA (server detail → CA
certificate), re-run Step 3, and replace the Authorization header value. No Gleipnir restart is
needed, because the PEM and headers are part of the client cache key. Policies, the protocol
pin, and the Run attribution setting (it is on the Gleipnir row, not the Relay side) all
survive.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| TLS "hostname mismatch" | URL host ≠ Relay `-hostname`. |
| "unknown authority" / wrong pinned CA | Stale CA after a reset. |
| `401` | Token expired (24h) or wiped by a reset. |
| Relay role error on `run_operation` | The Account is a `viewer`. |
| Badge Legacy / unknown | Rediscover. |
| Agent reports `pending_approval` | The gate rule is out-of-band, or the Relay lacks #646. |
| Approval answered but refused (not in audience / requester excluded) | The gate rule lacks audience `gleipnir` or `requester_allowed` true. |
| Tool call times out after approval | The `relay` server's Call timeout is not `120s` (detail modal shows `Default (30s)`). Set it there; no restart needed. |
| Relay shows no on-behalf-of / session ref | Run attribution is `Off` on the `relay` server's detail. Set it to `Relay preset`; no restart needed. |
| Run fails with feedback timeout | Nobody with the `approver` role answered within `feedback.timeout`. |
| `spawn systemctl` failure | The demo fleet's busybox limit. |
| `data.warnings` about `params` | Server not discovered before the POST. |
| Webhook `401` / `403` / `409` / `filtered` | As in devops. |
