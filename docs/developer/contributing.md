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

Package boundaries are intentional. `internal/mcp` must have no import dependencies on `internal/agent`. The poll trigger engine reuses the MCP client directly — a tight coupling here would require refactoring later.

See [`architecture.md`](architecture.md) for the full package layout.

## ADRs

Architectural decisions are tracked in [`docs/ADR_Tracker.md`](../ADR_Tracker.md). When you make an architectural decision:

1. Add an entry to the tracker.
2. Reference the ADR number in your commit message and PR description.
3. Do not reference ADRs from inside source code — they belong in commit history and the tracker.

## Pull requests

- Keep PRs focused. One concern per PR.
- Tests pass locally before opening: `go test ./...` and `npx vitest run` from `frontend/`.
- Reference the issue you're addressing in the PR title or description.
