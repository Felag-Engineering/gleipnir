#!/usr/bin/env bash
set -euo pipefail

# Reject any tracked file that contains private key material.
#
# A Minisign secret key (plugins/slack/slack.key) sat unencrypted in this
# public repo for three months even though plugins/slack/.gitignore ignored
# *.key: it had been force-added. .gitignore cannot stop that, so this lint
# checks what is actually tracked.
#
# Detected, only when the marker is a whole line (as it is in a real key
# file), so test code that embeds a dummy header inside a string literal does
# not trip it:
#   - Minisign / gleipnir-plugin secret keys: "untrusted comment: ... secret key"
#   - PEM private keys: "-----BEGIN [<ALG> ]PRIVATE KEY-----" (PKCS#8, RSA, EC,
#     OPENSSH, ENCRYPTED)
#
# Usage: lint-secret-keys.sh [dir]   (default: the git work tree root)
# With a dir argument it scans files on disk under dir instead of `git ls-files`,
# which is what the self-test uses.

pattern='^(untrusted comment: .*secret key.*|-----BEGIN ([A-Z0-9]+ )?PRIVATE KEY-----)[[:space:]]*$'

if [ "$#" -gt 0 ]; then
    offending=$(grep -rlIE "$pattern" -- "$1" || true)
else
    offending=$(git ls-files -z | xargs -0 grep -lIE "$pattern" -- 2>/dev/null || true)
fi

if [ -n "$offending" ]; then
    echo "error: private key material is committed:" >&2
    printf '  %s\n' $offending >&2
    echo >&2
    echo "Remove the file and treat the key as compromised: deleting it does not" >&2
    echo "un-publish it (git history, clones, forks). Generate a new key and keep" >&2
    echo "it out of the repo, e.g. in a password manager or CI secret." >&2
    exit 1
fi
