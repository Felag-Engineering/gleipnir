#!/usr/bin/env bash
set -euo pipefail

# Proves lint-profile-vocab.sh fails on a vendor name in normative text and
# passes on annotated lines and on text after an Examples heading. A lint that
# silently matched nothing would otherwise look identical to a clean repo.

here="$(cd "$(dirname "$0")" && pwd)"
fixtures="$here/../tests/lint-fixtures/profile-vocab"
lint="$here/lint-profile-vocab.sh"

if "$lint" --files "$fixtures/violating.md" >/dev/null 2>&1; then
    echo "self-test FAILED: lint passed on a vendor name in normative text" >&2
    exit 1
fi

if "$lint" --files "$fixtures/empty-reason.md" >/dev/null 2>&1; then
    echo "self-test FAILED: lint accepted a vocab-allow with no reason" >&2
    exit 1
fi

# Go files are scanned whole (no Examples cutoff) and use the // marker.
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
printf 'package x\n\n// Posts to Telegram.\n' >"$tmp/bad.go"
if "$lint" --files "$tmp/bad.go" >/dev/null 2>&1; then
    echo "self-test FAILED: lint passed on a vendor name in Go" >&2
    exit 1
fi
printf 'package x\n\n// Posts to Telegram. // vocab-allow: fixture reason\n' >"$tmp/ok.go"

if ! "$lint" --files "$fixtures/allowed.md" "$tmp/ok.go" >/dev/null 2>&1; then
    echo "self-test FAILED: lint rejected an annotated line or text under ## Examples" >&2
    exit 1
fi

echo "lint-profile-vocab self-test: ok"
