#!/usr/bin/env bash
set -euo pipefail

# Keep vendor names out of normative profile / extension text.
#
# A profile or extension contract has to be implementable by any plugin. The
# moment the normative text names a particular product ("post to Slack", "as
# GitHub does"), that product's behaviour quietly becomes the definition and
# every other implementer is guessing. Vendor names belong in examples and
# appendices, which are explicitly non-normative.
#
# Scanned:
#   markdown  everything before the first "## Appendix" or "## Examples" heading
#   Go        the whole file, EXCEPT *_test.go: tests legitimately use vendor
#             names as fixtures, and they are not contract text a plugin author
#             reads. Production Go under the listed dirs (conformance harness
#             messages, SDK docs) is contract-adjacent, so it is scanned.
# Denylist: scripts/profile-vocab-denylist.txt (case-insensitive whole words).
# Opt-out for a genuine need (or a false positive such as a test matrix), on
# the offending line, with a non-empty reason:
#   <!-- vocab-allow: reason -->      (markdown)
#   // vocab-allow: reason            (Go)
#
# Usage:
#   lint-profile-vocab.sh                 scan the repo's normative paths
#   lint-profile-vocab.sh --files F...    scan exactly these files (self-test);
#                                         *_test.go is not skipped here
# Globs that match nothing are tolerated; some of these paths may not exist yet.

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
denylist="$here/profile-vocab-denylist.txt"

patterns="$(mktemp)"
trap 'rm -f "$patterns"' EXIT
grep -vE '^[[:space:]]*(#|$)' "$denylist" >"$patterns"

# Matches a vocab-allow marker followed by at least one real reason character
# (not just the closing "-->" of an HTML comment).
allow_re='vocab-allow:[[:space:]]*([^[:space:]-]|-[^>-])'

violations=0

scan_file() {
    local file="$1" limit hits
    case "$file" in
    *.md)
        # Last line to scan: the one before the first Appendix/Examples heading.
        limit=$(awk '/^## (Appendix|Examples)/ { print NR - 1; exit }' "$file")
        limit="${limit:-999999999}"
        ;;
    *) limit=999999999 ;;
    esac

    # Blank out this repo's own Go module path: "github.com/..." in an import
    # is a location, not a vendor reference. Line numbers are unaffected.
    hits=$(head -n "$limit" "$file" | sed 's#github\.com/felag-engineering/#module/#g' | grep -inwf "$patterns" | grep -ivE "$allow_re" || true)
    if [ -n "$hits" ]; then
        violations=1
        while IFS= read -r line; do
            printf '%s:%s\n' "$file" "$line" >&2
        done <<<"$hits"
    fi
}

files=()
if [ "${1:-}" = "--files" ]; then
    shift
    files=("$@")
else
    cd "$root"
    shopt -s nullglob globstar
    for f in \
        docs/developer/extension-io-gleipnir-*.md \
        docs/developer/profile-*.md \
        docs/developer/host-endpoint-contract.md \
        docs/developer/plugin-manifest-v2.md \
        docs/proposals/io-gleipnir-*.md \
        conformance/*/CHECKLIST.md \
        plugin-sdk/conform/checklists/*.md \
        plugin-sdk/conform/**/*.go \
        plugin-sdk/channelext/**/*.go \
        plugin-sdk/mcpserver/**/*.go \
        conformance/**/*.go; do
        case "$f" in
        *_test.go) continue ;;
        esac
        [ -f "$f" ] && files+=("$f")
    done
fi

for f in ${files[@]+"${files[@]}"}; do
    scan_file "$f"
done

if [ "$violations" -ne 0 ]; then
    echo >&2
    echo "error: vendor names in normative profile/extension text (see scripts/lint-profile-vocab.sh)." >&2
    echo "Reword vendor-neutrally, move the text under an '## Examples' / '## Appendix' heading," >&2
    echo "or annotate the line with 'vocab-allow: <reason>'." >&2
    exit 1
fi
