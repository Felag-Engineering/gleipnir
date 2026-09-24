# Relay smoke test

The cross-product smoke suite (issue #931, part of #927): a build-tagged Go test,
`internal/relaysmoke` (build tag `relaysmoke`), that drives an in-process Gleipnir through Relay's
real MCP registration, RunLauncher, and tool-input HTTP stack, against a live, compose-started
Relay demo fleet.

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
5. **A gated mutate.** A scripted `run_operation` (`service.restart` on a fan-out-2 selector)
   against the lane's own in-band mutate rule (see [The gate override](#the-gate-override)
   below). See [The MRTR skip](#the-mrtr-skip) for what actually happens on a Relay build
   without MRTR support.
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
- MRTR coverage (branch A of `gated_mutate`) depends on
  [gleipnir-relay#646](https://github.com/Felag-Engineering/gleipnir-relay/issues/646), which is
  not in the demo fleet's Relay build as of this writing. See [The MRTR skip](#the-mrtr-skip).
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

This brings up its own compose project, `gleipnir-relaysmoke`, with the control plane republished
on `127.0.0.1:19443` (override with `RELAYSMOKE_CONTROL_PORT`) instead of Relay's usual
`7654`/`9443` — it can run **beside** a developer's own `relay-demo` fleet with no port conflict
and no shared volumes. `scripts/relaysmoke.sh` never calls Relay's own `scripts/demo-fleet.sh`:
that script's `reset` runs `down -v` against the fixed `relay-demo` project, which would destroy a
presenter's live fleet days before they need it.

`RELAYSMOKE_KEEP=1` skips teardown (containers keep running; only the exported credential files
under the artifact dir are still deleted, since the fleet can re-export them). Artifacts
(`report.json`, `go-test.log`, and — on failure — `relay-fleet.log` / `relay-fleet-ps.txt`) land
in `RELAYSMOKE_ARTIFACT_DIR` (default: a fresh `mktemp -d`), printed at the end of the run.

## Reading a failure

| Failure message names | Cause | To confirm it deliberately |
|---|---|---|
| `CA configuration: ...` | `discovery_error` contains `TLS certificate verification failed` | Point `RELAYSMOKE_CA_FILE` at a different PEM before running the suite. |
| `bearer token: ...` | `discovery_error` contains `status 401` (or `403`) | Substitute an invalid token in the machine Account mint step, or edit the registration body in a local run. |
| `negotiated protocol version = ... want "2026-07-28"` | Relay's `server/discover` did not negotiate the modern transport | This is the intended signal if Relay's SDK regresses to the legacy pin — it is not a Gleipnir-side bug to silence. |
| `mutate run ended without parking and without pending_approval` | Gleipnir's parked-result (`input_required`) handling | Break `decodeInputRequiredResult` locally against a Relay branch that implements #646, and confirm this message (not a skip) is what fires. |
| `fleet did not reach N connected Nodes within 3m` | `waitForFleet` — the compose fleet did not finish enrolling | Check `relay-fleet.log` / `relay-fleet-ps.txt` in the artifact dir. |

## The MRTR skip

`t.Run("gated_mutate")` detects which of two things actually happened, rather than assuming one:

- **Branch A — Relay parked with `input_required`.** The lane approves it through
  `POST /api/v1/runs/{runID}/tool-input`, asserts the run completes, and asserts a decision
  record. This requires gleipnir-relay#646.
- **Branch B — the run completed with the universal `pending_approval` result text instead.**
  This is what happens on every Relay build without #646 (the demo fleet, as of this writing).
  The lane asserts the fallback shape (`matched_rules` contains the lane's own gate rule,
  `request_id`/`plan_hash` non-empty), denies the parked request through Relay's Control API as
  cleanup, and then calls `t.Skipf`, naming the Relay SHA and gleipnir-relay#646 in the skip
  message.

A skip is not a pass: `report.json`'s `mrtr` field and the test's own step summary both say
`skipped: pending_approval fallback`, not `exercised`. Anything that is neither of the above (the
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

Delete this override once Relay's own demo fixture ships an in-band mutate rule of its own.

## CI

There is currently no CI leg for this lane, and no `.github/workflows/relay-smoke.yml`.
`gleipnir-relay` is a private repository, and running this suite in Gleipnir's own CI needs a
Relay source checkout — either a deploy key against the private repo, or published images. The
nightly/on-demand workflow is deferred until Relay publishes images to GHCR, requested in
[gleipnir-relay#451, item 5](https://github.com/Felag-Engineering/gleipnir-relay/issues/451#issuecomment-5813323627).
Until then, running this lane is a local/manual step (`make relaysmoke RELAY_DIR=...`), and the
Relay SHA it was last run against is tracked in `internal/relaysmoke/testdata/relay-ref` (bump it
in a one-line PR after confirming the new SHA's `docker-compose.yml` still has `profiles: ["demo"]`).

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
