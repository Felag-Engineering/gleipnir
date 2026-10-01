# Relay smoke test

The cross-product smoke suite (issue #931, part of #927): a build-tagged Go test,
`internal/relaysmoke` (build tag `relaysmoke`), that drives an in-process Gleipnir through Relay's
real MCP registration, RunLauncher, and tool-input HTTP stack, against a live, compose-started
Relay demo fleet.

> **Before a demo, run `make relaysmoke-demo RELAY_DIR=<gleipnir-relay checkout>` and do not go
> on stage unless it passes.** It is the only automated check that Relay's in-band approval
> question reaches a human in Gleipnir and the answered retry runs the mutate, against Relay's own
> demo gate. See [Strict mode](#strict-mode-the-pre-demo-gate).

For bring-up, SAN, and trust-model facts (the `/etc/hosts` mapping, why the control-plane
certificate has a single DNS SAN `relay`, the CA export, minting a machine Account, and the demo
fleet's approval gate) see [`docs/playbooks/fleet-ops/README.md`](../playbooks/fleet-ops/README.md)
— this document does not repeat them, only what differs for an automated lane.

## What it proves

1. **CA + bearer registration.** `POST /api/v1/mcp/servers` with the exported CA PEM and a
   freshly-minted machine Account's bearer token succeeds with no `discovery_error`. It also
   sets `call_timeout_seconds: 120` in the same request and asserts
   `effective_call_timeout_seconds == 120` in the response — this lane is also a live check of
   the per-server call timeout override (#939), which Relay needs because an approved retry runs
   the Job synchronously inside the retried `tools/call`.
2. **Discovery and canonicalization.** All eight of Relay's tools
   (`list_nodes`, `describe_node`, `list_operations`, `run_operation`, `raw_exec`, `get_job`,
   `cancel_job`, `approve_request`) appear, each with a non-null, valid-JSON canonical schema,
   and a second live `RefreshTools` call diffs empty against the first.
3. **The negotiated MCP protocol version, printed and asserted.** Gleipnir's client is bilingual
   and Relay's SDK will move independently; a silent downgrade to the legacy transport would keep
   every other subtest green while quietly disabling the in-band approval flow. The lane logs it
   (`RELAYSMOKE negotiated MCP protocol version: ...`) and fails if it is not `2026-07-28`.
4. **The reader flow.** A scripted `testutil.MockLLMClient` calls `list_nodes` then a read-class
   `run_operation` (`file.read /etc/os-release`) across the whole fleet, and every Node returns
   `success`.
5. **A gated mutate.** A scripted `run_operation` (`service.restart` of `nginx` on
   `node:role=web, node:env=prod` — fan-out 2, the demo's own repair beat) against the lane's
   own in-band mutate rule (see [The gate override](#the-gate-override) below), or Relay's own
   demo gate in [strict mode](#strict-mode-the-pre-demo-gate). See
   [The MRTR skip](#the-mrtr-skip) for what happens when Relay does not ask the question.
6. **A `raw_exec` every Node Policy refuses**, called directly through an admin-credentialed
   `mcp.Client` (never through an agent — no demo policy grants `raw_exec`, matching ADR-001):
   parks under the raw_exec gate rule, is approved out-of-band, and the identical retry comes
   back `denied_by_policy` on every Node with a non-empty `stderr` and `refusal_explanation`.
7. **Negative controls, every run.** A server registered with the wrong CA fails with
   `TLS certificate verification failed`; one registered with an invalid bearer token fails with
   `status 401`. Both are asserted to be true every night, not demonstrated once — this is
   acceptance bullet 2 ("breaking the CA/token fails with a message naming the cause").

## What it does NOT prove

- It drives Gleipnir **in-process**, not the Docker image `docker build` produces. Image
  packaging is out of scope; the issue's subject is the protocol integration.
- In the **default** lane, MRTR coverage (branch A of `gated_mutate`) is not guaranteed: a
  pending_approval fallback skips. Only [strict mode](#strict-mode-the-pre-demo-gate) turns
  that into a failure. See [The MRTR skip](#the-mrtr-skip).
- It does not attest the demo's UI, or anything about the frontend.

## Running locally

Prerequisites: Docker Engine with the Compose v2 plugin (>= 2.24.4 — the compose override uses
`ports: !override`), `python3`, and a [gleipnir-relay](https://github.com/Felag-Engineering/gleipnir-relay)
checkout. Add the `/etc/hosts` line once, the same one
[fleet-ops](../playbooks/fleet-ops/README.md#step-1--bring-up-the-relay-fleet) and the Relay
runbook both require:

```console
$ grep -q 'relay$' /etc/hosts || echo '127.0.0.1 relay' | sudo tee -a /etc/hosts
```

Then:

```console
$ make relaysmoke RELAY_DIR=~/felag/gleipnir-relay
```

It layers its override onto Relay's fleet compose file, `docker-compose.demo.yml` (Relay split the
deployment stack out of a root `docker-compose.yml` in gleipnir-relay#783; the script falls back to
`docker-compose.yml` only for an older checkout), with `--profile demo` — the same nine-Node fleet
`make demo-reset` brings up. This brings up its own compose project, `gleipnir-relaysmoke`, with the control plane republished
on `127.0.0.1:19443` (override with `RELAYSMOKE_CONTROL_PORT`) instead of Relay's usual
`7654`/`9443` — it can run **beside** a developer's own `relay-demo` fleet with no port conflict
and no shared volumes. `scripts/relaysmoke.sh` never calls Relay's own `scripts/demo-fleet.sh`:
that script's `reset` runs `down -v` against the fixed `relay-demo` project, which would destroy a
presenter's live fleet days before they need it.

`RELAYSMOKE_KEEP=1` skips teardown (containers keep running; only the exported credential files
under the artifact dir are still deleted, since the fleet can re-export them). Artifacts
(`report.json`, `go-test.log`, and — on failure — `relay-fleet.log` / `relay-fleet-ps.txt`) land
in `RELAYSMOKE_ARTIFACT_DIR` (default: a fresh `mktemp -d`), printed at the end of the run.

## Strict mode (the pre-demo gate)

```console
$ make relaysmoke-demo RELAY_DIR=~/felag/gleipnir-relay
# equivalently: RELAYSMOKE_REQUIRE_MRTR=1 make relaysmoke RELAY_DIR=...
```

`RELAYSMOKE_REQUIRE_MRTR=1` changes two things and nothing else; the default lane
(`make relaysmoke`) behaves exactly as before.

1. **The Relay runs its own demo gate.** `scripts/relaysmoke.sh` sets
   `DEV_FLEET_APPROVAL_GATE_CONFIG=/etc/relay/approval-gates.demo.json` — the gate file baked into
   Relay's dev-fleet image, and exactly what Relay's `scripts/demo-fleet.sh` selects for the
   presenter's `relay-demo` fleet: `demo-fleet-wide-mutates-need-a-human` (mutate, fan-out >= 2)
   and `demo-raw-exec-always-needs-a-human`, both 1 approval, `in-band`,
   `requester_allowed: true`, no audience. Pointing at the baked file rather than copying its
   rules into `testdata/` is deliberate: the pre-demo gate cannot drift from the stage gate. (The
   lane's `testdata/approval-gates.json` mount is still applied, over a path the Relay then never
   reads.) The script refuses to start if the checkout predates the demo gate
   (gleipnir-relay#774), since the Relay would otherwise quietly load a different one.
2. **`gated_mutate`'s pending_approval fallback (Branch B) fails the run** instead of skipping,
   and Branch A additionally asserts the whole demo beat:
   - the question arrives as `input_required` (a `tool_input.created` pause, `permission` kind,
     answerable by `approver`);
   - it is answered through Gleipnir's operator path, `POST /api/v1/runs/{id}/tool-input`, with
     the same body the run page's Approve button sends — never `approve_request`, which the mutate
     policy does not grant (the trace is asserted to contain no such call);
   - the retry carried the responder assertion: Relay's `decision` block on the answered retry is
     `channel: in-band`, `state: approved`, and `on_behalf_of` equals the Gleipnir user who
     answered. On an API-token session Relay accepts an answer only with
     `_meta["io.gleipnir/responder"] = {gate: "permission", username}` (AP-64), and echoes that
     username here — so this is the wire-level proof, read from Relay's side. An
     `answer_refused` block fails the run naming the responder stamp as the likely cause;
   - Relay's own record (`GET /api/v1/approvals/{id}`) matched the demo rule, with the demo
     requirement (in-band, `requester_allowed`, no audience), one in-band decision by the
     `gleipnir-smoke` machine Account, and the same Job the result reports;
   - `service.restart` of `nginx` returned `success` with `exit_code` 0 on **each** of the two
     Nodes. `node:role=web, node:env=prod` is `daemon-4` and `daemon-5` of the `demo` profile; both
     are `role=web`, so Relay's `docker/daemon-entrypoint.sh` links its DEV-ONLY `systemctl` shim
     there and `policy.web.toml` allows `nginx`. (A `role=db` Node would fail with
     `spawn systemctl`; `role=bastion` is `denied_by_policy` — neither is in this Selector.)

`report.json` records `strict_mrtr`, `approval_gate`, `mrtr` (`exercised`, or a `failed: ...`
reason) and `mutate_outcomes`. The rest of the suite runs unchanged, except that
`raw_exec_denied_by_policy` expects the demo gate's raw-exec rule name; its direct client
declares no elicitation capability, so Relay still renders `pending_approval` for it under the
in-band rule, and it is still approved out-of-band by `dev-approver`.

What strict mode still does not prove: the agent's account name (the demo uses
`auto-incident-response`; the lane mints `gleipnir-smoke`, also a machine `operator`), Gleipnir's
UI, or the Gleipnir Docker image — see [What it does NOT prove](#what-it-does-not-prove).

## Reading a failure

| Failure message names | Cause | To confirm it deliberately |
|---|---|---|
| `CA configuration: ...` | `discovery_error` contains `TLS certificate verification failed` | Point `RELAYSMOKE_CA_FILE` at a different PEM before running the suite. |
| `bearer token: ...` | `discovery_error` contains `status 401` (or `403`) | Substitute an invalid token in the machine Account mint step, or edit the registration body in a local run. |
| `negotiated protocol version = ... want "2026-07-28"` | Relay's `server/discover` did not negotiate the modern transport | This is the intended signal if Relay's SDK regresses to the legacy pin — it is not a Gleipnir-side bug to silence. |
| `mutate run ended without parking and without pending_approval` | Gleipnir's parked-result (`input_required`) handling | Break `decodeInputRequiredResult` locally against a Relay branch that implements #646, and confirm this message (not a skip) is what fires. |
| `STRICT: Relay ... answered the gated mutate with pending_approval` | Relay did not ask in-band: the gate is not askable by the requesting Account, Gleipnir stopped declaring client name `gleipnir` or the `elicitation` capability, or the Relay build lacks #646 | Run strict mode against a Relay checkout older than gleipnir-relay#774 (no in-band demo gate). |
| `Relay refused the answered retry` | Relay's `answer_refused` — most likely the retry no longer carries the `io.gleipnir/responder` permission assertion | Drop the responder `_meta` in `internal/execution/agent/inputrequired.go` locally. |
| `decision.on_behalf_of = ...` | The responder username Relay recorded is not the Gleipnir user who answered | — |
| `node ... outcome = failure ... want success` | The restart did not succeed on a web Node — e.g. the `systemctl` shim is missing from Relay's daemon image | Check `relay-fleet.log` for `systemctl shim installed` on `daemon-4`/`daemon-5`. |
| `fleet did not reach N connected Nodes within 3m` | `waitForFleet` — the compose fleet did not finish enrolling | Check `relay-fleet.log` / `relay-fleet-ps.txt` in the artifact dir. |

## The MRTR skip

`t.Run("gated_mutate")` detects which of two things actually happened, rather than assuming one:

- **Branch A — Relay parked with `input_required`.** The lane approves it through
  `POST /api/v1/runs/{runID}/tool-input`, asserts the run completes, and asserts a decision
  record. This requires gleipnir-relay#646.
- **Branch B — the run completed with the universal `pending_approval` result text instead.**
  This is what happens on every Relay build without #646, and — on current Relay — against the
  lane's own `testdata/approval-gates.json` too: its mutate rule names audience `dev-approver`
  and leaves `requester_allowed` at its strict default, so Relay's askability rule (an in-band
  answer is authenticated as the requesting `gleipnir-smoke` Account) never lets it ask. That
  is why [strict mode](#strict-mode-the-pre-demo-gate) runs Relay's own demo gate instead.
  The lane asserts the fallback shape (`matched_rules` contains the lane's own gate rule,
  `request_id`/`plan_hash` non-empty), denies the parked request through Relay's Control API as
  cleanup, and then calls `t.Skipf`, naming the Relay SHA and gleipnir-relay#646 in the skip
  message.

A skip is not a pass: `report.json`'s `mrtr` field and the test's own step summary both say
`skipped: pending_approval fallback`, not `exercised`. In strict mode the same outcome is a
`t.Fatalf` (after the same cleanup), and `mrtr` reads `failed: pending_approval fallback (strict)`.
Anything that is neither of the above (the
run fails, or completes with something that is not `pending_approval`) is a hard `t.Fatalf`
naming Gleipnir's parked-result handling as the cause — a Gleipnir-side MRTR regression can never
hide inside the skip. Once gleipnir-relay#646 lands, check out its branch (or its merged SHA) and
run `make relaysmoke RELAY_DIR=<that checkout>` to confirm Branch A runs unskipped before bumping
the pin.

## The gate override

The suite mounts its own approval-gate file
(`internal/relaysmoke/testdata/approval-gates.json`) over Relay's baked-in
`docker/fixtures/approval-gates.json`, entirely inside its own private compose project — it never
touches a developer's `relay-demo` fleet or Relay's source. The override exists because the
shipped demo fixture's fleet-wide-mutate rule is `out-of-band`, and gleipnir-relay#646 requires an
out-of-band rule to never emit an answerable elicitation ("Must hold": *"For an out-of-band rule,
emit no question the client could answer"*). Against the stock fixture, `gated_mutate`'s Branch A
could therefore never run on ANY Relay build, even a #646-complete one. The override's mutate rule
is `channel: in-band` instead, with the same audience (`dev-approver`) and everything else
unchanged; its raw_exec rule stays `out-of-band` (keeping the stock fixture's own rule name,
`dev-raw-exec-always-needs-a-human`) so `raw_exec_denied_by_policy` is deterministic across every
Relay build regardless of #646.

Deciding a parked request through the Control API (`decide` in this suite) is always an
**out-of-band** decision by construction — it lands on an authenticated Relay endpoint the
requesting session's credential does not carry — and an out-of-band decision always satisfies an
in-band-required rule (Relay's channel strength ordering makes out-of-band the *stronger* of the
two). So the override changes nothing about how `raw_exec_denied_by_policy`'s or Branch B's own
cleanup decisions are approved; it only changes what an in-band (MRTR) answer would additionally
satisfy, once #646 ships.

Relay's demo fixture now ships an in-band gate of its own (`approval-gates.demo.json`,
gleipnir-relay#774), which strict mode uses directly. The default lane keeps this override for now
so its behaviour is unchanged; note that this override's mutate rule is not askable by the lane's
own requesting Account (see [The MRTR skip](#the-mrtr-skip)), so retiring it in favour of the demo
gate is the natural follow-up.

## CI

There is currently no CI leg for this lane, and no `.github/workflows/relay-smoke.yml`.
`gleipnir-relay` is a private repository, and running this suite in Gleipnir's own CI needs a
Relay source checkout — either a deploy key against the private repo, or published images. The
nightly/on-demand workflow is deferred until Relay publishes images to GHCR, requested in
[gleipnir-relay#451, item 5](https://github.com/Felag-Engineering/gleipnir-relay/issues/451#issuecomment-5813323627).
Until then, running this lane is a local/manual step (`make relaysmoke RELAY_DIR=...`), and the
Relay SHA it was last run against is tracked in `internal/relaysmoke/testdata/relay-ref` (bump it
in a one-line PR after confirming the new SHA's `docker-compose.demo.yml` still has `profiles: ["demo"]`
and that `make relaysmoke-demo` passes against it).

## Why ci-local never runs it

Every file in `internal/relaysmoke` carries `//go:build relaysmoke`, including `doc.go` — with
every file tagged, `go build/vet/test/list ./...` does not see the package at all (unlike
`internal/plugin/substrate`, where `doc.go` is deliberately untagged so `go list ./...` still
shows the package with `[no test files]`). `scripts/ci-local-scope.sh` needs no special case for
this: a change under `internal/relaysmoke/` still resolves to the Go package
`internal/relaysmoke` by directory, but that import path is absent from `go list -e ./...`
(every file is tag-excluded), so it is never emitted as something the race lane needs to test —
pinned by `scripts/ci-local-scope-self-test.sh`. What DOES run locally and in every PR is a
compile-only guard: `make lint-relaysmoke-build` (`go vet -tags relaysmoke
./internal/relaysmoke/`, part of `ci-local-lint`) and a PR-only `relaysmoke-build` job in
`.github/workflows/ci.yml`, mirroring `lint-substrate-build`/`substrate-build`. Both need no
Docker and no Relay checkout; they exist so a change to `internal/mcp`, `internal/execution/run`,
`internal/http/api`, or `internal/testutil` cannot silently break this suite's compile between
runs of the real lane.
