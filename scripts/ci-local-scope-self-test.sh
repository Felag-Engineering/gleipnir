#!/usr/bin/env bash
set -uo pipefail

# Proves scripts/ci-local-scope.sh narrows the gate without losing coverage.
#
# The scoper decides how much of the local merge gate runs, so a silent bug in
# it would look exactly like a green gate. These cases pin the properties that
# make a narrowed run trustworthy: it never drops the changed package itself, it
# always pulls in reverse dependencies, and anything it cannot reason about
# widens back to the full gate.
#
# Assertions are on properties, not on exact package lists — the lists change
# every time an import is added, and a self-test that has to be edited for
# unrelated refactors stops being read.

scope() { CI_LOCAL_SCOPE_FILES="$1" ./scripts/ci-local-scope.sh 2>/dev/null; }
field() { printf '%s\n' "$1" | sed -n "s/^$2=//p"; }

failures=0
check() { # check <description> <condition-result-rc>
	if [ "$2" -eq 0 ]; then
		echo "  ok   — $1"
	else
		echo "  FAIL — $1" >&2
		failures=$((failures + 1))
	fi
}

mod=$(GOWORK=off go list -m 2>/dev/null)

echo "ci-local-scope self-test:"

# 1. Changing the gate itself invalidates every assumption the scoper makes.
out=$(scope "Makefile")
[ "$(field "$out" CI_LOCAL_SCOPE)" = "full" ]
check "a Makefile change forces the full gate" $?

out=$(scope "go.mod")
[ "$(field "$out" CI_LOCAL_SCOPE)" = "full" ]
check "a root go.mod change forces the full gate" $?

out=$(scope "scripts/ci-local.sh")
[ "$(field "$out" CI_LOCAL_SCOPE)" = "full" ]
check "a scripts/ change forces the full gate" $?

# 2. A leaf package must bring itself *and* its reverse dependencies. internal/
#    schemanorm is imported by internal/mcp (see internal/schemanorm/CLAUDE.md),
#    so a change to it that did not re-test internal/mcp would be unsound.
out=$(scope "internal/schemanorm/normalize.go")
pkgs=$(field "$out" CI_LOCAL_GO_PKGS)
[ "$(field "$out" CI_LOCAL_SCOPE)" = "scoped" ]
check "a leaf package change narrows the gate" $?
printf '%s' "$pkgs" | grep -q "$mod/internal/schemanorm"
check "the changed package itself is always tested" $?
printf '%s' "$pkgs" | grep -q "$mod/internal/mcp"
check "a direct reverse dependency (internal/mcp) is pulled in" $?
# internal/execution/agent does not import schemanorm; it reaches it only
# through internal/mcp. Asserting on a *direct* importer would pass even with
# the transitive-closure scan deleted, because internal/mcp's own test files
# import schemanorm and the test-import pass would catch it anyway. This is the
# assertion that actually pins the closure.
printf '%s' "$pkgs" | grep -q "$mod/internal/execution/agent"
check "a transitive reverse dependency (internal/execution/agent) is pulled in" $?
[ "$(printf '%s' "$pkgs" | wc -w)" -lt "$(GOWORK=off go list ./... 2>/dev/null | wc -l)" ]
check "the narrowed set is strictly smaller than ./..." $?

# 3. go:embed assets have no .go sibling; they must resolve to the package that
#    embeds them, not be dropped.
out=$(scope "internal/db/migrations/0099_synthetic.sql")
printf '%s' "$(field "$out" CI_LOCAL_GO_PKGS)" | grep -q "$mod/internal/db"
check "an embedded .sql asset maps to its embedding package" $?

# 4. Files under no Go package cannot affect a Go build, and must not drag the
#    root package in by walking up to the repo root.
out=$(scope "docs/developer/architecture.md")
[ -z "$(field "$out" CI_LOCAL_GO_PKGS)" ]
check "a docs-only change tests no Go packages" $?
[ "$(field "$out" CI_LOCAL_RUN_FRONTEND)" = "0" ]
check "a docs-only change skips the frontend lane" $?

# 5. Per-lane selection.
out=$(scope "frontend/src/pages/Tools.tsx")
[ "$(field "$out" CI_LOCAL_RUN_FRONTEND)" = "1" ] && [ -z "$(field "$out" CI_LOCAL_GO_PKGS)" ]
check "a frontend-only change runs the frontend lane and no Go tests" $?

out=$(scope "plugin-sdk/plugin.go")
[ "$(field "$out" CI_LOCAL_RUN_SDK)" = "1" ] && [ -n "$(field "$out" CI_LOCAL_PLUGIN_DIRS)" ]
check "an SDK change re-tests the SDK and every first-party plugin" $?

out=$(scope "internal/schemanorm/normalize.go")
[ "$(field "$out" CI_LOCAL_RUN_SDK)" = "0" ]
check "a root-module change does not drag in the SDK lane" $?

# 6. A multi-area diff is the union of its parts, never one of them.
out=$(scope "$(printf 'frontend/src/x.tsx\ninternal/schemanorm/normalize.go')")
[ "$(field "$out" CI_LOCAL_RUN_FRONTEND)" = "1" ] &&
	printf '%s' "$(field "$out" CI_LOCAL_GO_PKGS)" | grep -q "$mod/internal/schemanorm"
check "a mixed diff selects every affected lane" $?

echo
if [ "$failures" -ne 0 ]; then
	echo "ci-local-scope self-test FAILED ($failures)" >&2
	exit 1
fi
echo "ci-local-scope self-test OK"
