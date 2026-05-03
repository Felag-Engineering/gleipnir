#!/usr/bin/env bash
set -euo pipefail

# Reject any Go file under ROOT (default: plugins/) whose imports reference
# the host module outside of /plugin-sdk/. Plugins under /plugins/ may only
# import the Go stdlib, third-party deps, and github.com/felag-engineering/
# gleipnir/plugin-sdk/...; anything else under github.com/felag-engineering/
# gleipnir/ is a boundary violation (see ADR-041 / docs/developer/contributing.md).

root="${1:-plugins}"

# If the directory is missing or contains no Go files, the lint is a no-op.
# This is the expected state until the first reference plugin lands (#173).
if [ ! -d "$root" ]; then
    exit 0
fi
files=$(find "$root" -type f -name '*.go' 2>/dev/null)
if [ -z "$files" ]; then
    exit 0
fi

# Tight regex: a real import path appears either inside a parenthesized
# import block (line starts with whitespace + `"`) OR as a single-line
# import (line starts with `import` + whitespace + `"`). Anchoring on
# either form eliminates false positives from comments, string literals,
# and trailing comments that mention the module path.
offending=$(printf '%s\0' $files \
    | xargs -0 grep -nHE '^([[:space:]]+|import[[:space:]]+)"github\.com/felag-engineering/gleipnir/' \
    | grep -vE '"github\.com/felag-engineering/gleipnir/plugin-sdk/' \
    || true)

if [ -n "$offending" ]; then
    echo "Plugin import boundary violation:" >&2
    echo "  Files under $root/ may only import the Go stdlib, third-party" >&2
    echo "  packages, and github.com/felag-engineering/gleipnir/plugin-sdk/..." >&2
    echo "  See docs/developer/contributing.md → 'Plugin import boundary'." >&2
    echo "" >&2
    echo "$offending" >&2
    exit 1
fi
