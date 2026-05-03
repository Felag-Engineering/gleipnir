#!/usr/bin/env bash
# Proves scripts/lint-plugins.sh fires on a deliberate boundary violation.
set -uo pipefail

fixture="tests/lint-fixtures/plugins-forbidden-import"
set +e
./scripts/lint-plugins.sh "$fixture" >/dev/null 2>&1
rc=$?
set -e

if [ "$rc" -eq 0 ]; then
    echo "self-test FAILED: lint did not catch the deliberate violation in $fixture" >&2
    exit 1
fi
echo "self-test OK: lint correctly rejected the fixture (exit $rc)"
