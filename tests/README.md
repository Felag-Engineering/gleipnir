# tests/

This directory holds **linter test fixtures**, NOT Go tests. Go tests live
alongside production code as `*_test.go` files per Go convention.

Subdirectories under `tests/lint-fixtures/` deliberately violate one or more
project lint rules so the lint scripts can be self-tested in CI.
