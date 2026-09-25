#!/usr/bin/env bash
set -euo pipefail

# Proves lint-secret-keys.sh fails on real-looking key files and passes on the
# string-literal form test code legitimately uses. A lint that silently
# matched nothing would otherwise look identical to a clean repo.

here="$(cd "$(dirname "$0")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

expect_fail() {
    if "$here/lint-secret-keys.sh" "$tmp" >/dev/null 2>&1; then
        echo "self-test FAILED: lint passed on $1" >&2
        exit 1
    fi
    rm -rf "${tmp:?}"/*
}

printf 'untrusted comment: gleipnir-plugin secret key 05c85b92fa859345\nRWQAAAA\n' >"$tmp/plugin.key"
expect_fail "a minisign secret key file"

printf -- '-----BEGIN PRIVATE KEY-----\nMIIEvQ\n-----END PRIVATE KEY-----\n' >"$tmp/key.pem"
expect_fail "a PKCS#8 PEM private key"

printf -- '-----BEGIN OPENSSH PRIVATE KEY-----\nb3Blbn\n-----END OPENSSH PRIVATE KEY-----\n' >"$tmp/id_ed25519"
expect_fail "an OpenSSH private key"

# Must pass: a public key, and a dummy header inside a Go string literal.
printf 'untrusted comment: gleipnir-plugin public key 05c85b92fa859345\nRWQAAAA\n' >"$tmp/plugin.pub"
printf '\tpem: "-----BEGIN PRIVATE KEY-----\\nAAAA\\n-----END PRIVATE KEY-----\\n",\n' >"$tmp/x_test.go"
if ! "$here/lint-secret-keys.sh" "$tmp" >/dev/null 2>&1; then
    echo "self-test FAILED: lint rejected a public key or a string-literal header" >&2
    exit 1
fi

echo "lint-secret-keys self-test: ok"
