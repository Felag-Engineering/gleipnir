#!/usr/bin/env bash
# relaysmoke.sh — brings up a private Relay demo fleet, runs the
# `relaysmoke`-tagged Go suite against it, and tears the fleet back down
# (issue #931, part of #927). DEV/CI-only: nothing in `make build/test/lint`
# calls this, and it is never part of `ci-local` (Docker + a multi-container
# fleet do not belong in the fast inner loop). See docs/developer/relay-smoke.md.
#
# WHY A PRIVATE PROJECT NAME. Relay's own scripts/demo-fleet.sh is hardwired
# to the compose project `relay-demo`, and its own `reset` runs `down -v` —
# wiping the CA and every Node identity. Calling it from here would risk
# destroying a developer's OWN live demo fleet days before they need it on
# stage. This script therefore never calls demo-fleet.sh and never touches
# the `relay-demo` project: every `docker compose` call in this file goes
# through dc(), pinned to a project name of its own, `gleipnir-relaysmoke`.
#
# WHY THE COMPOSE OVERRIDE DROPS THE FIXED HOST PORTS. The override file
# (internal/relaysmoke/testdata/compose.override.yml) republishes the
# control plane on a DIFFERENT host port (19443 by default, `!override`),
# so this script's fleet can run beside a developer's own `relay-demo`
# fleet without a port conflict.
set -euo pipefail

die() {
	local code="$1"
	shift
	echo "relaysmoke: $*" >&2
	exit "$code"
}

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)" || exit 1
readonly REPO_ROOT

# --- Inputs ------------------------------------------------------------
[ -n "${RELAY_DIR:-}" ] || die 2 "RELAY_DIR is required — set it to a gleipnir-relay checkout, e.g. RELAY_DIR=~/felag/gleipnir-relay"
[ -d "$RELAY_DIR" ] || die 2 "RELAY_DIR=$RELAY_DIR is not a directory"
[ -f "$RELAY_DIR/docker-compose.yml" ] || die 2 "RELAY_DIR=$RELAY_DIR has no docker-compose.yml — is this a gleipnir-relay checkout?"
RELAY_DIR="$(cd "$RELAY_DIR" && pwd)"
readonly RELAY_DIR

RELAYSMOKE_CONTROL_PORT="${RELAYSMOKE_CONTROL_PORT:-19443}"
RELAYSMOKE_WAIT_SECONDS="${RELAYSMOKE_WAIT_SECONDS:-900}"
RELAYSMOKE_ARTIFACT_DIR="${RELAYSMOKE_ARTIFACT_DIR:-$(mktemp -d -t relaysmoke.XXXXXX)}"
RELAYSMOKE_KEEP="${RELAYSMOKE_KEEP:-0}"
RELAYSMOKE_RELAY_REF="${RELAYSMOKE_RELAY_REF:-$(git -C "$RELAY_DIR" rev-parse HEAD)}"
mkdir -p "$RELAYSMOKE_ARTIFACT_DIR"
readonly RELAYSMOKE_CONTROL_PORT RELAYSMOKE_WAIT_SECONDS RELAYSMOKE_ARTIFACT_DIR RELAYSMOKE_KEEP RELAYSMOKE_RELAY_REF

echo "relaysmoke: artifacts land in $RELAYSMOKE_ARTIFACT_DIR"

# --- Preflight -----------------------------------------------------------
command -v docker >/dev/null || die 2 "docker is required"
docker compose version >/dev/null 2>&1 || die 2 "docker compose (the v2 plugin) is required"
command -v python3 >/dev/null || die 2 "python3 is required (readiness parsing)"

# `ports: !override` needs Compose >= 2.24.4.
compose_version="$(docker compose version --short 2>/dev/null || echo 0.0.0)"
if ! python3 -c "
import sys
def parts(v):
    return tuple(int(p) for p in v.split('.')[:3])
sys.exit(0 if parts('$compose_version') >= (2, 24, 4) else 1)
" 2>/dev/null; then
	die 2 "docker compose $compose_version is too old — this lane's compose.override.yml uses 'ports: !override', which needs Compose >= 2.24.4"
fi

if ! getent hosts relay >/dev/null 2>&1; then
	die 2 "'relay' does not resolve — Relay's control-plane certificate has a single DNS SAN, 'relay'. Add the hosts entry the Relay runbook requires: grep -q 'relay\$' /etc/hosts || echo '127.0.0.1 relay' | sudo tee -a /etc/hosts"
fi

readonly PROJECT="gleipnir-relaysmoke"

# Every compose invocation goes through this one function (mirrors Relay's
# own single-call-site discipline in scripts/demo-fleet.sh).
dc() {
	docker compose -p "$PROJECT" --project-directory "$RELAY_DIR" \
		-f "$RELAY_DIR/docker-compose.yml" \
		-f "$REPO_ROOT/internal/relaysmoke/testdata/compose.override.yml" \
		--profile demo "$@"
}

if docker ps -a --filter "publish=${RELAYSMOKE_CONTROL_PORT}" --format '{{.Names}}' | grep -qv "^${PROJECT}-"; then
	die 2 "host port ${RELAYSMOKE_CONTROL_PORT} is already published by a container outside this lane's own project ($PROJECT) — set RELAYSMOKE_CONTROL_PORT to a free port"
fi

pinned_ref="$(cat "$REPO_ROOT/internal/relaysmoke/testdata/relay-ref" 2>/dev/null || true)"
head_ref="$(git -C "$RELAY_DIR" rev-parse HEAD 2>/dev/null || echo unknown)"
if [ -n "$pinned_ref" ] && [ "$head_ref" != "$pinned_ref" ]; then
	echo "relaysmoke: WARNING: RELAY_DIR is at $head_ref, the pinned SHA is $pinned_ref (internal/relaysmoke/testdata/relay-ref) — continuing against what's checked out" >&2
fi

# Copy the gate file into the artifact dir with a fixed, non-group/world-
# writable mode: Relay's approval-gate loader refuses a group/world-writable
# config file, and a checkout under a permissive umask must not make it do
# that here.
gates_file="$RELAYSMOKE_ARTIFACT_DIR/approval-gates.json"
install -m 0644 "$REPO_ROOT/internal/relaysmoke/testdata/approval-gates.json" "$gates_file"
export RELAYSMOKE_GATES_FILE="$gates_file"
export RELAYSMOKE_CONTROL_PORT

cred_dir="$RELAYSMOKE_ARTIFACT_DIR/credentials"
mkdir -m 0700 -p "$cred_dir"

cleanup() {
	local exit_code=$?
	if [ "$exit_code" -ne 0 ]; then
		dc logs --no-color --timestamps >"$RELAYSMOKE_ARTIFACT_DIR/relay-fleet.log" 2>&1 || true
		dc ps -a >"$RELAYSMOKE_ARTIFACT_DIR/relay-fleet-ps.txt" 2>&1 || true
	fi
	if [ "$RELAYSMOKE_KEEP" != "1" ]; then
		dc down -v --remove-orphans >/dev/null 2>&1 || true
	fi
	# Credentials are removed even with RELAYSMOKE_KEEP=1 -- the fleet, if
	# kept, can re-export them; a live admin/approver token has no business
	# surviving in a temp dir on disk longer than this run needs it.
	rm -rf "$cred_dir"
	echo "relaysmoke: artifacts in $RELAYSMOKE_ARTIFACT_DIR"
	exit "$exit_code"
}
trap cleanup EXIT

echo "relaysmoke: bringing up project $PROJECT (control plane on 127.0.0.1:${RELAYSMOKE_CONTROL_PORT})"
dc down -v --remove-orphans >/dev/null 2>&1 || true
dc up --build -d

# --- Readiness -----------------------------------------------------------
daemon_services="$(dc config --services | grep '^daemon-' | sort || true)"
[ -n "$daemon_services" ] || die 1 "docker compose config --services returned no daemon-* services — does RELAY_DIR's docker-compose.yml still define the 'demo' profile?"
expected_nodes="$(printf '%s\n' "$daemon_services" | wc -l | tr -d ' ')"

read -r -d '' WAIT_HEALTHY_PY <<'PY' || true
import json, os, sys


def load_items(raw):
    raw = raw.strip()
    if not raw:
        return []
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        items = []
        for line in raw.splitlines():
            line = line.strip()
            if line:
                items.append(json.loads(line))
        return items
    return parsed if isinstance(parsed, list) else [parsed]


items = load_items(sys.stdin.read())
by_service = {i.get("Service"): i for i in items}

daemons = os.environ.get("DAEMON_SERVICES", "").split()
oneshots = ["credential", "approver", "console-admin", "console-password", "bootstrap"]

missing = []
for svc in ["relay"] + daemons:
    item = by_service.get(svc)
    if not item or item.get("State") != "running" or item.get("Health") != "healthy":
        missing.append(svc)

failed = []
not_done = []
for svc in oneshots:
    item = by_service.get(svc)
    if not item:
        not_done.append(svc)
        continue
    if item.get("State") != "exited":
        not_done.append(svc)
        continue
    if str(item.get("ExitCode")) != "0":
        failed.append(svc)

if failed:
    print(",".join(failed))
    sys.exit(2)
if missing or not_done:
    print(",".join(missing + not_done))
    sys.exit(1)
sys.exit(0)
PY
readonly WAIT_HEALTHY_PY

echo "relaysmoke: waiting up to ${RELAYSMOKE_WAIT_SECONDS}s for $PROJECT to become healthy ($expected_nodes daemon services)..."
deadline=$((SECONDS + RELAYSMOKE_WAIT_SECONDS))
while :; do
	json="$(dc ps -a --format json 2>/dev/null)" || json=""
	rc=0
	out="$(DAEMON_SERVICES="$daemon_services" python3 -c "$WAIT_HEALTHY_PY" <<<"$json")" || rc=$?
	if [ "$rc" -eq 0 ]; then
		break
	fi
	if [ "$rc" -eq 2 ]; then
		die 1 "one-shot(s) [$out] exited non-zero; inspect with: docker compose -p $PROJECT logs SERVICE"
	fi
	if [ "$SECONDS" -ge "$deadline" ]; then
		dc ps -a || true
		die 1 "$PROJECT did not become healthy within ${RELAYSMOKE_WAIT_SECONDS}s (still waiting on: $out)"
	fi
	sleep 2
done
echo "relaysmoke: fleet healthy"

# --- Export CA + credentials ---------------------------------------------
dc cp relay:/var/lib/relay/ca/ca-cert.pem "$cred_dir/ca-cert.pem"

read_credential() {
	local file="$1" dst="$2"
	dc run --rm --no-deps --entrypoint sh approver -c "cat /credential/$file" | tr -d '\r' >"$dst"
	chmod 0600 "$dst"
	test -s "$dst" || die 1 "credential file $file came back empty"
}
read_credential control-credential "$cred_dir/control-credential"
read_credential approver-credential "$cred_dir/approver-credential"

# --- Run the suite ---------------------------------------------------------
export RELAYSMOKE_MCP_URL="https://relay:${RELAYSMOKE_CONTROL_PORT}/mcp"
export RELAYSMOKE_API_BASE="https://relay:${RELAYSMOKE_CONTROL_PORT}"
export RELAYSMOKE_CA_FILE="$cred_dir/ca-cert.pem"
export RELAYSMOKE_ADMIN_TOKEN_FILE="$cred_dir/control-credential"
export RELAYSMOKE_APPROVER_TOKEN_FILE="$cred_dir/approver-credential"
export RELAYSMOKE_RELAY_REF
export RELAYSMOKE_REPORT="$RELAYSMOKE_ARTIFACT_DIR/report.json"
export RELAYSMOKE_EXPECTED_NODES="$expected_nodes"

echo "relaysmoke: running the suite (relay $RELAYSMOKE_RELAY_REF, $expected_nodes Nodes expected)"
set +e
(
	cd "$REPO_ROOT" &&
		GOWORK=off go test -tags relaysmoke -count=1 -timeout 25m -v ./internal/relaysmoke/ 2>&1 | tee "$RELAYSMOKE_ARTIFACT_DIR/go-test.log"
	exit "${PIPESTATUS[0]}"
)
test_status=$?
set -e

exit "$test_status"
