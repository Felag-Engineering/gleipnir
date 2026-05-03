# Plugin import boundary — lint fixture

This directory exists solely as input for `scripts/lint-plugins-self-test.sh`.
Do not add real code here. The Go file deliberately violates the /plugins/
import boundary; the `//go:build never` tag prevents it from compiling.
