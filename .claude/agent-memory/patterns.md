# Gleipnir — Recurring Code Patterns & Conventions

## Error handling
- Errors wrapped with `fmt.Errorf("context: %w", err)` — never bare returns
- `ValidationError` type collects multiple validation errors as `[]string`; returned as `*ValidationError`
- Service layer wraps DB errors: `fmt.Errorf("create policy: %w", err)`

## Validation pattern
- `Validate(p *model.ParsedPolicy) error` collects all errors before returning
- Sub-validators return `[]string`; top-level Validate aggregates them
- Type `Valid()` methods on model enums used for enum validation

## Service layer
- Parse → Validate → (non-blocking check) → Store is the established pattern for policy write ops
- `ToolLookup` interface injected into Service; nil-safe (skips check if nil)
- `checkToolRefs` issues warnings, not errors — save succeeds even if tools are unknown
- `SaveResult` returns `model.Policy` (domain type), not `db.Policy` — service owns the DB-to-domain mapping

## Parser
- rawPolicy structs used as intermediate YAML representation (unexported)
- Defaults applied in convert* functions, not in the raw structs
- `strings.TrimSpace` applied to preamble and task fields
- `on_timeout`/`timeout` only populated when `approval: required`; zero-valued for `approval: none`

## Renderer
- `RenderSystemPrompt` returns a string (no error) — pure function
- Capabilities block generated at run start, never persisted (ADR-012)
- `strings.Builder` used for string accumulation

## Tests
- Table-driven tests used for multi-case branching
- `validPolicy()` helper returns a minimal valid policy for mutation in validator tests
- `assertValidationContains(t, p, substr)` helper pattern for validator tests
- In-memory SQLite (`":memory:"`) used for service integration tests
- `stubLookup` struct implements `ToolLookup` for test isolation

## Model enums
- Each enum type has `Valid() bool` and `String() string` methods
- `ApprovalMode`, `OnTimeout`, `ConcurrencyPolicy`, `TriggerType`, `CapabilityRole`, etc.

## UpdatePolicy SQL
- Updates `name`, `trigger_type`, `yaml`, and `updated_at` — keeps indexed routing columns in sync
