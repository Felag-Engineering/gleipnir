#!/usr/bin/env bash
set -euo pipefail

# Work out the smallest set of ci-local lanes that can still catch a regression
# from the current diff, and print it as a KEY=VALUE block for scripts/ci-local.sh.
#
# WHY THIS EXISTS
# The heavy half of the gate is `go test -race ./...`: it links one test binary
# per package (53 of them in the root module) at 150-690 MiB per concurrent
# link. In CI that is one 16 GiB runner finishing in ~4 minutes. Locally it is
# the single reason the gate needs a RAM budget and a machine-wide lock at all,
# and — because a fresh dev-loop worktree shares no build cache with the main
# tree — the reason the go-build cache grows by ~1 GiB per wave.
#
# Nearly all of that work cannot fail. A diff that touches internal/llm/contract
# cannot break internal/plugin/dispatch. So the default gate now races only the
# packages the diff can actually reach, and CI keeps running the full matrix on
# the pushed branch.
#
# WHAT IS DELIBERATELY *NOT* SCOPED
#   - `go build ./...` stays whole-tree. It links no test binaries, so it is
#     cheap next to the race lane, and "everything still compiles" is the
#     property most worth keeping unconditionally local.
#   - The lint and drift lanes stay whole-tree. They are seconds, and the drift
#     lanes are the one thing CI genuinely cannot do for an uncommitted tree.
#
# FAIL-SAFE, NOT FAIL-FAST
# Every case this script cannot reason about widens the scope rather than
# narrowing it: no merge-base, a change to the gate itself, a change to the
# root go.mod. A wrong answer here must cost time, never coverage.

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

# Resolve the default branch rather than assuming "main", so the scope is
# computed against the right base in a fork or a renamed-branch checkout.
base_ref="origin/main"
if default_ref=$(git symbolic-ref --quiet refs/remotes/origin/HEAD 2>/dev/null); then
	base_ref="${default_ref#refs/remotes/}"
fi

all_plugin_dirs=$(find plugins -maxdepth 1 -mindepth 1 -type d 2>/dev/null | sort | tr '\n' ' ')

emit_full() {
	echo "CI_LOCAL_SCOPE=full"
	echo "CI_LOCAL_SCOPE_REASON=$1"
	echo "CI_LOCAL_GO_PKGS=./..."
	echo "CI_LOCAL_RUN_SDK=1"
	echo "CI_LOCAL_RUN_FRONTEND=1"
	echo "CI_LOCAL_RUN_SUBSTRATE=1"
	echo "CI_LOCAL_PLUGIN_DIRS=${all_plugin_dirs}"
	exit 0
}

# Testing/debug seam: when set, this newline-separated file list stands in for
# the git query. scripts/ci-local-scope-self-test.sh drives the scoper through
# it, and it also answers "what would the gate do for this change?" without
# needing a tree in that state. It narrows nothing on its own — the lanes it
# selects still run in full, and CI still runs the whole matrix regardless.
if [ -n "${CI_LOCAL_SCOPE_FILES:-}" ]; then
	changed_files=$(printf '%s\n' "$CI_LOCAL_SCOPE_FILES" | sort -u | grep -v '^$' || true)
else
	if ! base=$(git merge-base HEAD "$base_ref" 2>/dev/null) || [ -z "$base" ]; then
		emit_full "no merge-base against ${base_ref} — cannot tell what changed"
	fi

	# Committed-since-base, staged, unstaged, and untracked, unioned. Deliberately
	# not `git status --porcelain`: its rename entries ("R old -> new") need parsing,
	# and these three plumbing commands need none.
	changed_files=$(
		{
			git diff --name-only "$base" --
			git diff --name-only HEAD --
			git ls-files --others --exclude-standard
		} | sort -u | grep -v '^$' || true
	)
fi

if [ -z "$changed_files" ]; then
	emit_full "no diff against ${base_ref} — nothing to narrow to"
fi

# Changing the gate, its inputs, or the root module graph invalidates the
# premise that a package's reverse-dependency set bounds the blast radius.
if printf '%s\n' "$changed_files" | grep -qE '^(Makefile|scripts/|\.github/workflows/|sqlc\.yaml|buf\.yaml|buf\.gen\.yaml|go\.mod|go\.sum)'; then
	emit_full "diff touches the build/gate definition or the root module graph"
fi

run_frontend=0
run_sdk=0
run_substrate=0
plugin_dirs=""
root_go_changed=""

# Packages the real-daemon integration suite exercises (issue #820). A change
# to any of them can only be proven against an actual runtime — the whole point
# of that suite is the questions container.Fake cannot answer: whether a
# network created Internal really has no route out, whether a subnet the
# allocator carved is one the daemon accepts, whether an image GC believes is
# unreferenced is one the daemon will actually delete.
#
# Listed by directory prefix rather than derived from the import graph on
# purpose. The suite imports internal/plugin/{container,reconciler} directly, so
# a graph-derived set would be exactly these two — but the lane also has to fire
# for egress and resources, whose behaviour it depends on WITHOUT importing (the
# proxy env a container is created with, the cgroup caps it is created under).
# A rule that only followed imports would silently stop covering them.
substrate_dirs="
internal/plugin/substrate
internal/plugin/container
internal/plugin/reconciler
internal/plugin/egress
internal/plugin/resources
"

while IFS= read -r f; do
	case "$f" in
	frontend/*)
		run_frontend=1
		continue
		;;
	plugin-sdk/*)
		run_sdk=1
		continue
		;;
	# The workspace sum only affects the workspace-mode build, which is the SDK
	# lane. The root lanes run GOWORK=off and cannot see it.
	go.work | go.work.sum)
		run_sdk=1
		continue
		;;
	plugins/*)
		d=${f#plugins/}
		d="plugins/${d%%/*}"
		case " $plugin_dirs " in
		*" $d "*) ;;
		*) plugin_dirs="$plugin_dirs $d" ;;
		esac
		continue
		;;
	esac
	# Substrate membership is by prefix and does NOT `continue`: these are
	# ordinary root-module packages that must still be raced like any other.
	# The lane is additive, never a substitute for the unit coverage.
	while IFS= read -r d; do
		[ -z "$d" ] && continue
		case "$f" in
		"$d"/*) run_substrate=1 ;;
		esac
	done <<<"$substrate_dirs"

	root_go_changed="$root_go_changed$f"$'\n'
done <<<"$changed_files"

# A first-party plugin builds against the SDK, so an SDK change has to re-test
# them even when the plugin itself is untouched.
if [ "$run_sdk" = "1" ]; then
	plugin_dirs="$all_plugin_dirs"
fi

# Map each changed root-module file to the Go package that contains it, walking
# up until a directory with .go files is found. This is what makes go:embed
# assets work without a special case: internal/db/migrations/0007_x.sql has no
# .go sibling, so it resolves to internal/db — the package that embeds it.
#
# Known gap: deleting a package's *last* .go file leaves no directory to resolve
# to, so that package contributes nothing here. Its importers stop compiling,
# and `go build ./...` — which stays whole-tree precisely for cases like this —
# fails the gate before the race lane's coverage would have mattered.
if ! module_path=$(GOWORK=off go list -m 2>/dev/null) || [ -z "$module_path" ]; then
	# Without the module path nothing below can map a file to a package, and the
	# result would be an empty package set — i.e. a silently *skipped* race lane
	# on a diff that does touch Go. Widen instead.
	emit_full "could not resolve the module path — refusing to narrow"
fi

changed_pkgs=""
if [ -n "$root_go_changed" ]; then
	while IFS= read -r f; do
		[ -z "$f" ] && continue
		dir=$(dirname "$f")
		if [ "$dir" = "." ]; then
			# A file sitting at the repo root belongs to the root package if that
			# package exists at all.
			compgen -G "*.go" >/dev/null 2>&1 && changed_pkgs="$changed_pkgs$module_path"$'\n'
			continue
		fi
		# Walk up out of asset subdirectories, but never past the top level: a
		# go:embed directive can only reach files inside its own package
		# directory, so an ancestor walk always terminates at the embedding
		# package. Reaching "." instead means the file is under no Go package at
		# all (docs/, schemas/) and cannot affect a build.
		while [ "$dir" != "." ]; do
			if compgen -G "$dir/*.go" >/dev/null 2>&1; then
				changed_pkgs="$changed_pkgs$module_path/$dir"$'\n'
				break
			fi
			dir=$(dirname "$dir")
		done
	done <<<"$root_go_changed"
fi

changed_pkgs=$(printf '%s' "$changed_pkgs" | sort -u | grep -v '^$' || true)

go_pkgs=""
if [ -n "$changed_pkgs" ]; then
	# .Deps is the *transitive* build-dependency list, so one scan of it yields
	# the whole reverse-dependency closure. .TestImports/.XTestImports are direct
	# only, which is why they need the second pass below.
	if ! pkg_graph=$(GOWORK=off go list -e -f '{{.ImportPath}}|{{range .Deps}}{{.}} {{end}}|{{range .TestImports}}{{.}} {{end}}{{range .XTestImports}}{{.}} {{end}}' ./... 2>/dev/null); then
		emit_full "could not load the package graph — refusing to guess at scope"
	fi

	go_pkgs=$(awk -F'|' '
		NR==FNR { changed[$0]=1; next }
		{ imp[FNR]=$1; deps[FNR]=$2; timp[FNR]=$3; n=FNR }
		END {
			# A package is affected if it *is* a changed package, or if any of
			# its transitive build deps changed.
			for (i = 1; i <= n; i++) {
				if (imp[i] in changed) { aff[imp[i]] = 1; continue }
				split(deps[i], d, " ")
				for (j in d) if (d[j] in changed) { aff[imp[i]] = 1; break }
			}
			# A test binary is affected if it imports an affected package, even
			# when the package under test is not. Because .Deps above was already
			# transitive, anything reachable *through* a test import is in aff or
			# changed by now, so a single extra pass closes the set.
			for (i = 1; i <= n; i++) {
				if (imp[i] in aff) continue
				split(timp[i], t, " ")
				for (j in t) if ((t[j] in aff) || (t[j] in changed)) { aff[imp[i]] = 1; break }
			}
			for (k in aff) print k
		}
	' <(printf '%s\n' "$changed_pkgs") <(printf '%s\n' "$pkg_graph") | sort | tr '\n' ' ')
fi

echo "CI_LOCAL_SCOPE=scoped"
echo "CI_LOCAL_SCOPE_REASON=narrowed to the packages this diff can reach"
echo "CI_LOCAL_GO_PKGS=${go_pkgs}"
echo "CI_LOCAL_RUN_SDK=${run_sdk}"
echo "CI_LOCAL_RUN_FRONTEND=${run_frontend}"
echo "CI_LOCAL_RUN_SUBSTRATE=${run_substrate}"
echo "CI_LOCAL_PLUGIN_DIRS=${plugin_dirs# }"
