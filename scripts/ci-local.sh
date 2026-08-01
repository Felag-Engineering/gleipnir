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
# Overrides (both intended for hosts with RAM to spare):
#   CI_LOCAL_JOBS=N     lane parallelism; skips the RAM-derived budget
#   CI_LOCAL_NO_LOCK=1  don't serialize against other worktrees

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

lock_file="${TMPDIR:-/tmp}/gleipnir-ci-local-$(id -u).lock"
lock_wait_seconds=3600

run_lanes() {
	echo "ci-local: $jobs lane(s) in parallel, go test -p $CI_LOCAL_TEST_P" >&2
	# MAKEFLAGS is cleared so an outer `make -jN ci-local` cannot leak its own
	# job count (or its jobserver) into the lanes and undo the budget.
	env -u MAKEFLAGS -u MAKELEVEL -u MFLAGS \
		"$make_bin" --no-print-directory -j"$jobs" -O ci-local-lanes
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
