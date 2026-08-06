#!/usr/bin/env bash
set -euo pipefail

# Memory-budgeted, machine-wide-serialized entry point for the `ci-local` gate.
#
# WHY THIS EXISTS
# In CI every ci-local lane is its own 16 GiB runner. Locally they all land on
# one machine, and the heavy ones are memory-bound, not CPU-bound (measured on
# a 4-core / 4 GiB host):
#
#   tsc --noEmit          ~530 MiB   one process, not tunable
#   staticcheck           ~370 MiB   whole-program analysis
#   go link               150-690 MiB *per concurrent link* — `go test ./...`
#                                     links one test binary per package
#   race test binary      ~165 MiB each
#
# Sizing parallelism by `nproc` ignores all of that. `make -j4 -O ci-local`
# measured a 4044 MiB peak (RAM+swap) on that host — it completes only because
# it swaps. Two gates at once does not fit, and the dev-loop runs pipelines
# from several worktrees concurrently: the kernel OOM-killed a gate mid-run at
# a 3.4G RSS + 2.5G swap peak.
#
# An OOM-killed gate is worse than a slow one, because it is indistinguishable
# from a failing one. So:
#
#   1. lane parallelism is derived from RAM, not from core count, and
#   2. the gate takes an exclusive machine-wide lock, so a second worktree
#      queues behind the first instead of racing it for memory.
#
# The other half of the fix is scripts/ci-local-scope.sh: the race lane now
# covers only the packages the diff can reach, so the common inner-loop run
# links a handful of test binaries instead of 53. That shrinks the peak the
# budget below has to defend against, but does not remove the need for it — a
# diff that touches a core package still fans out to most of the tree.
#
# Overrides:
#   CI_LOCAL_JOBS=N     lane parallelism; skips the RAM-derived budget
#   CI_LOCAL_NO_LOCK=1  don't serialize against other worktrees
#   CI_LOCAL_FULL=1     no narrowing — every lane, every package

make_bin="${1:-make}"

# One heavy lane's peak, rounded up.
mib_per_lane=1200
# Held back for everything that is not the gate: OS, editor server, and any
# agent session driving the gate (~900 MiB measured on the host above).
mib_reserved=1024

jobs="${CI_LOCAL_JOBS:-}"
if [ -z "$jobs" ]; then
	mem_total_mib=$(awk '/^MemTotal:/ { print int($2 / 1024) }' /proc/meminfo 2>/dev/null || echo 0)
	cores=$(nproc 2>/dev/null || echo 4)
	if [ "$mem_total_mib" -le 0 ]; then
		jobs=$cores # no /proc/meminfo (non-Linux): fall back to core sizing
	else
		jobs=$(( (mem_total_mib - mib_reserved) / mib_per_lane ))
	fi
	[ "$jobs" -gt "$cores" ] && jobs=$cores
	# There are only five lanes; past 4 the gate is bound by the slowest one.
	[ "$jobs" -gt 4 ] && jobs=4
	[ "$jobs" -lt 1 ] && jobs=1
fi

# `go test -p` is how many packages compile/link/run at once *within* a lane,
# so it multiplies against the lane count — bound it by the same budget. This
# is a scheduling knob only: GOMAXPROCS and `-parallel` are untouched, so tests
# still run under the same concurrency CI gives them and the local gate stays
# as strong a race check as the remote one.
export CI_LOCAL_TEST_P="$jobs"

# Narrow the gate to the diff. Everything the scoper reports is exported so the
# Makefile lanes can branch on it; see scripts/ci-local-scope.sh for the rules.
scope="full"
scope_reason="CI_LOCAL_FULL=1"
if [ -z "${CI_LOCAL_FULL:-}" ]; then
	scope_env=$("$(dirname "$0")/ci-local-scope.sh")
	while IFS='=' read -r key value; do
		[ -z "$key" ] && continue
		case "$key" in
		CI_LOCAL_SCOPE) scope="$value" ;;
		CI_LOCAL_SCOPE_REASON) scope_reason="$value" ;;
		*) export "$key=$value" ;;
		esac
	done <<<"$scope_env"
fi

# substrate_status reports the real-daemon lane, which ci-local NEVER runs.
#
# The lane needs a working rootless-Podman socket and a pre-pulled probe image,
# and it creates real containers and networks on the developer's machine. Making
# the local gate depend on that would make `make ci-local` fail for reasons that
# have nothing to do with the diff — so it is CI's, and the local gate says so
# rather than staying quiet about a lane it did not run.
#
# Escape hatch, documented in docs/developer/container-substrate.md:
#   go test -tags substrate -count=1 ./internal/plugin/substrate/
substrate_status() {
	if [ "${CI_LOCAL_RUN_SUBSTRATE:-1}" = "1" ]; then
		echo "CI-only — this diff reaches it; run 'go test -tags substrate ./internal/plugin/substrate/' against a real socket"
	else
		echo "not reached by this diff"
	fi
}

# The gate's whole job is to be a trustworthy merge signal, so a narrowed run
# has to say so on the way in and on the way out. Never let "ci-local ✅" stand
# alone for a run that skipped lanes.
summarize() {
	local pkg_count="all"
	if [ "$scope" = "scoped" ]; then
		pkg_count=$(printf '%s' "${CI_LOCAL_GO_PKGS:-}" | wc -w | tr -d ' ')
	fi
	echo ""
	if [ "$scope" = "full" ]; then
		echo "ci-local ✅  FULL gate ($scope_reason)"
		echo "  lanes: backend build + race tests (all packages) · gofmt · staticcheck · plugin import boundary · gate scoper self-test · sqlc drift · proto lint+gen drift · plugin-sdk+examples+plugins · frontend typecheck+unit"
	else
		echo "ci-local ✅  SCOPED gate — $scope_reason"
		echo "  always run:  go build ./... · gofmt · staticcheck · plugin import boundary · gate scoper self-test · sqlc drift · proto lint+gen drift"
		echo "  race tests:  ${pkg_count} of $(GOWORK=off go list ./... 2>/dev/null | wc -l | tr -d ' ') root packages"
		echo "  sdk lane:    $([ "${CI_LOCAL_RUN_SDK:-1}" = "1" ] && echo "run" || echo "skipped — no plugin-sdk/, plugins/, or go.work change")"
		echo "  frontend:    $([ "${CI_LOCAL_RUN_FRONTEND:-1}" = "1" ] && echo "run" || echo "skipped — no frontend/ change")"
		echo "  substrate:   $(substrate_status)"
		echo "  ⚠ a scoped pass is an inner-loop signal, not full coverage — CI runs the whole matrix on the pushed branch."
		echo "  run 'make ci-local-full' for the unnarrowed gate."
	fi
	echo "not covered locally (CI-only): docker/arm64/podman image jobs, substrate real-daemon suite, vuln scans (make security)"
}

lock_file="${TMPDIR:-/tmp}/gleipnir-ci-local-$(id -u).lock"
lock_wait_seconds=3600

run_lanes() {
	if [ "$scope" = "full" ]; then
		echo "ci-local: FULL gate ($scope_reason)" >&2
	else
		echo "ci-local: scoped gate — $scope_reason" >&2
	fi
	echo "ci-local: $jobs lane(s) in parallel, go test -p $CI_LOCAL_TEST_P" >&2
	# MAKEFLAGS is cleared so an outer `make -jN ci-local` cannot leak its own
	# job count (or its jobserver) into the lanes and undo the budget.
	env -u MAKEFLAGS -u MAKELEVEL -u MFLAGS \
		"$make_bin" --no-print-directory -j"$jobs" -O ci-local-lanes
	summarize
}

if [ -n "${CI_LOCAL_NO_LOCK:-}" ]; then
	run_lanes
	exit
fi

exec 9>"$lock_file"
if ! flock -n 9; then
	echo "ci-local: another ci-local gate is running on this machine ($lock_file)." >&2
	echo "ci-local: waiting for it — running both at once would exhaust RAM." >&2
	if ! flock -w "$lock_wait_seconds" 9; then
		echo "ci-local: still locked after ${lock_wait_seconds}s; the holder may be wedged." >&2
		echo "ci-local: check for other 'make ci-local' processes, or set CI_LOCAL_NO_LOCK=1." >&2
		exit 1
	fi
fi
run_lanes
