# Contributing

## Code style

**Readable and understandable first.** Optimize for the next reader, not for cleverness or compactness.

**Explicit over clever.** When there's a straightforward way and a clever way, write the straightforward way.

**Strict error handling.** Never swallow errors. Wrap with context: `fmt.Errorf("context: %w", err)`.

**Tests alongside new code.** Use table-driven tests for anything with branching logic, error paths, or concurrency behavior. Don't test trivial getters. Do test:

- State machine transitions
- Error paths (missing tool, token budget exceeded, MCP server unreachable)
- Concurrent audit writes
- Context cancellation propagation

**Comments explain why, not what.** Non-obvious decisions get a brief inline comment. Architectural reasoning belongs in ADRs.

## Package boundaries

Package boundaries are intentional. `internal/mcp` must have no import dependencies on `internal/execution/agent`. The poll trigger engine reuses the MCP client directly — a tight coupling here would require refactoring later.

See [`architecture.md`](architecture.md) for the full package layout.
For plugin work, see [`plugin-system-spec.md`](plugin-system-spec.md) for the full design specification (process model, services, credentials, trust, observability).

## Plugin import boundary

Go files under `/plugins/` may only import:

- The Go standard library
- Third-party dependencies
- `github.com/felag-engineering/gleipnir/plugin-sdk/...`

Importing anything else under `github.com/felag-engineering/gleipnir/` (e.g. `internal/db`, `internal/execution/agent`) is a boundary violation. This rule keeps `/plugins/` self-contained so a future repo split (per the plugin system spec §14.7) stays a `git mv` rather than a refactor. As of v1.1.0 the plugins remain in the monorepo; the split is deferred.

**How to check locally:**

```bash
make lint-plugins
```

The rule is enforced by `scripts/lint-plugins.sh`. It scans `/plugins/` for import lines that reference host internals and exits non-zero on any match. If `/plugins/` does not yet exist the script exits 0 silently — this is expected today.

**Self-test:**

```bash
make lint-plugins-self-test
```

This passes the deliberate-violation fixture at `tests/lint-fixtures/plugins-forbidden-import/` to the script and asserts that the script correctly rejects it. Both `lint-plugins` and `lint-plugins-self-test` run as PR-only CI jobs.

## ADRs

Architectural decisions are tracked in [`ADR_Tracker.md`](ADR_Tracker.md). When you make an architectural decision:

1. Add an entry to the tracker.
2. Reference the ADR number in your commit message and PR description.
3. Do not reference ADRs from inside source code — they belong in commit history and the tracker.

## Pull requests

- Keep PRs focused. One concern per PR.
- Tests pass locally before opening: `go test ./...` and `npx vitest run` from `frontend/`.
- Reference the issue you're addressing in the PR title or description.
