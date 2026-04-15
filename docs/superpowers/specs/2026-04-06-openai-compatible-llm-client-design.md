# OpenAI-Compatible LLM Client — Design

**Issue:** #533
**Status:** Proposed
**Date:** 2026-04-06
**Related ADRs:** ADR-026 (Model-Agnostic Design), ADR-001 (Hard Capability Enforcement), ADR-008 (Approval Modes), ADR-017 (Parameter Scoping). Introduces ADR-032.

---

## 1. Summary

Add OpenAI as a first-class LLM provider in Gleipnir, implemented as a hand-rolled `internal/llm/openai` client speaking the OpenAI Chat Completions wire format. The same client also serves admin-managed instances pointed at any OpenAI-compatible backend (Ollama, vLLM, OpenRouter, Azure-via-compat, LM Studio, Together, Groq, etc.).

The feature introduces a second provider mechanism that coexists with the existing SDK-backed mechanism for Anthropic and Google. Anthropic and Google remain exactly as they are today; OpenAI-compatible providers are admin-managed at runtime via a new database table and admin UI section.

## 2. Goals and non-goals

### Goals

- Provide OpenAI Chat Completions support as a third first-class LLM provider in Gleipnir.
- Allow administrators to add, edit, test, and delete OpenAI-compatible provider instances at runtime, without restarting the server.
- Each instance has its own `name`, `base_url`, and encrypted API key. The same hand-rolled client implementation serves all instances.
- Policies reference providers by name exactly as they do today (`provider: <name>`). Policy authors see no infrastructure detail.
- Reuse the existing encryption (`internal/admin/crypto.go`, `GLEIPNIR_SECRET_KEY`) and the existing `ProviderRegistry` runtime-mutation API.
- Match the `LLMClient` interface contract exactly so the agent runtime is provider-agnostic.

### Non-goals (v1)

- The OpenAI Responses API (`/v1/responses`). Reasoning content from o-series models is not surfaced; only `reasoning_tokens` counts are recorded.
- Realtime, Assistants, Batch, Files, Embeddings — none are needed by Gleipnir's agent loop.
- Per-policy `base_url` overrides. Infrastructure config lives only on provider instances.
- Multiple instances of `anthropic` or `google`. Those remain single-keyed via the existing `system_settings` mechanism.
- Migration of existing Anthropic/Google secrets into the new table.
- A "this provider is referenced by N policies, are you sure?" check on delete. Deletion is destructive; broken policies fail at run-start with a clear error.
- Bulk import/export, pagination (single-digit instance counts expected), per-provider usage stats.
- Special-case support for no-auth backends. Admins enter a placeholder for the API key when targeting a no-auth Ollama or LM Studio.

## 3. Architecture

### 3.1 Two coexisting provider mechanisms

After this change, the registry contains entries from two distinct sources:

1. **SDK-backed fixed providers (`anthropic`, `google`).** Unchanged. Configured via the existing `system_settings` rows (`anthropic_api_key`, `google_api_key`), gated by the existing `knownProviders` slice in `internal/admin/handler.go`, registered via the existing `ProviderConfigurator` callback. Vendor SDKs justify per-provider client code because of features (prompt caching, signed thinking blocks, citations, structured outputs) that don't fit a uniform shape.
2. **OpenAI-compatible admin-managed instances.** New. Backed by a new SQLite table `openai_compat_providers`. Each row is a named instance with its own `base_url` and encrypted API key. Admins create, edit, and delete instances from a new section on the existing admin LLM Providers page. Each row registers itself in the `ProviderRegistry` under its user-chosen `name`, using a fresh `*openai.Client` constructed from the row's fields.

### 3.2 Reserved-name rule

The names `anthropic` and `google` are reserved at the API layer. An admin attempting to create an OpenAI-compatible provider with either name receives a 400 with a clear error message. This prevents shadowing the SDK providers in the registry.

### 3.3 Policy and capability semantics are unchanged

ADR-001 (hard capability enforcement), ADR-008 (policy-gated approvals), ADR-017 (parameter scoping), and ADR-018 (capability snapshot) all operate above the `LLMClient` interface. The OpenAI client is a leaf — it sees only what the agent runtime hands it, has no knowledge of policies, approvals, or scoping. From the policy author's perspective, an OpenAI-backed run is indistinguishable from an Anthropic or Google run except by the provider name.

### 3.4 Why hand-rolled, not the official OpenAI Go SDK

- The OpenAI Chat Completions wire format is small, stable, and re-implemented by dozens of third-party backends. A hand-rolled client (≈500 lines) is the right size.
- Permissive deserialization tolerates compat-backend quirks (omitted `usage` fields, slightly different streaming chunks, missing `/models`). A strict typed SDK would reject responses a permissive client accepts.
- Maintaining one client for both OpenAI proper and compat backends avoids the drift and bug-surface doubling of two parallel implementations of the same protocol.
- The SDK's value is concentrated in non-Chat-Completions surfaces (Realtime, Assistants, Responses) that Gleipnir does not need.

## 4. Data model

### 4.1 New table

Added to `schemas/sql_schemas.sql`:

```sql
CREATE TABLE openai_compat_providers (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT    NOT NULL UNIQUE,
    base_url          TEXT    NOT NULL,
    api_key_encrypted TEXT    NOT NULL,
    created_at        TEXT    NOT NULL,
    updated_at        TEXT    NOT NULL
);

CREATE INDEX idx_openai_compat_providers_name ON openai_compat_providers(name);
```

### 4.2 Field rules (enforced at the API layer)

- **`name`**: 1–64 chars, matches `^[a-z0-9][a-z0-9_-]*$` (lowercase letters, digits, `-`, `_`; first char must be alphanumeric). Must not equal `anthropic` or `google`. Uniqueness enforced by the SQL `UNIQUE` constraint.
- **`base_url`**: must parse as a valid `http://` or `https://` URL with no query string and no fragment. Trailing slashes are stripped on save. The URL is treated as the literal prefix and `/chat/completions`, `/models` are appended verbatim. The URL is not required to end in `/v1`.
- **`api_key_encrypted`**: ciphertext from `internal/admin.Encrypt`. Plaintext is never stored, never logged, never returned by any GET endpoint. The API layer requires a non-empty key on create and update. The client always sends `Authorization: Bearer <key>`. Admins targeting no-auth backends enter a placeholder.
- **`created_at` / `updated_at`**: RFC3339 strings, set by the handler.

### 4.3 sqlc queries

New file `internal/db/queries/openai_compat_providers.sql`:

- `ListOpenAICompatProviders` — all rows ordered by `name`
- `GetOpenAICompatProvider` — by `name`
- `GetOpenAICompatProviderByID` — by `id`
- `CreateOpenAICompatProvider` — insert, returns the new row
- `UpdateOpenAICompatProvider` — update by `id`, all fields including encrypted key
- `DeleteOpenAICompatProvider` — delete by `id`

There is no partial-update query. Edit always sends the full row. The API layer interprets a body whose `api_key` matches the masked value as "do not change the key" and keeps the existing ciphertext untouched (see §6.3).

## 5. Registry lifecycle

### 5.1 Startup

After Anthropic and Google are registered through their existing paths:

1. Call `ListOpenAICompatProviders(ctx)`.
2. For each row: decrypt the key, construct an `*openai.Client` with `base_url` + plaintext key, call `registry.Register(row.Name, client)`.
3. If decryption fails for a row (corrupt ciphertext, key rotation), log an error with the row name and skip it. Do not abort startup. The admin UI will surface the row with a "key unreadable, please re-enter" state.
4. If two rows somehow share a name (defensive — should be impossible due to `UNIQUE`), the second `Register` overwrites the first; log a warning.

The startup loader lives in `internal/llm/openai/loader.go` and is invoked from `main.go` next to the existing Anthropic/Google wiring.

### 5.2 On admin create / update

The handler:

1. Validates fields (name format, reserved names, URL shape, key non-empty).
2. Calls `verifyConnection(ctx, baseURL, apiKey)` — see §6.4.
3. Encrypts the (possibly new) key, writes the row.
4. Constructs a fresh `*openai.Client` and calls `registry.Register(name, client)`. On a name change, also `registry.Unregister(oldName)`.

### 5.3 On admin delete

The handler:

1. Deletes the row.
2. Calls `registry.Unregister(name)`.
3. Returns 204.

No check for policies referencing this provider. Policies that reference it will fail at run-start with the existing "unknown LLM provider" error from `ProviderRegistry.Get`. In-flight runs that already hold a client reference complete their current API call and only fail when subsequent runs try to look up the provider.

### 5.4 Concurrency

`ProviderRegistry`'s existing `sync.RWMutex` protects `Register`, `Unregister`, and `Get`. Admin mutations are serialized by the standard HTTP request lifecycle. There is no background reconciliation goroutine.

## 6. Admin API

### 6.1 New routes

All routes are gated by the existing admin-role middleware. All responses use the standard `{data: T}` / `{error, detail}` envelope.

| Method | Path | Purpose |
|---|---|---|
| `GET`    | `/api/v1/admin/openai-providers`            | List all rows. Returns name, base_url, masked_key, models_endpoint_available, timestamps. |
| `POST`   | `/api/v1/admin/openai-providers`            | Create. Body: `{name, base_url, api_key}`. Validates + tests + inserts + registers. |
| `GET`    | `/api/v1/admin/openai-providers/{id}`       | Get one row by id. |
| `PUT`    | `/api/v1/admin/openai-providers/{id}`       | Update. Body: `{name, base_url, api_key}`. See §6.3. |
| `DELETE` | `/api/v1/admin/openai-providers/{id}`       | Delete a row, unregister from registry. Returns 204. |
| `POST`   | `/api/v1/admin/openai-providers/{id}/test`  | Re-run the connection test against the stored row without modifying it. |

### 6.2 Request and response shapes

Create / update request:

```json
{
  "name": "openai",
  "base_url": "https://api.openai.com/v1",
  "api_key": "sk-..."
}
```

List / get response item:

```json
{
  "id": 1,
  "name": "openai",
  "base_url": "https://api.openai.com/v1",
  "masked_key": "sk-...wxyz",
  "models_endpoint_available": true,
  "created_at": "2026-04-06T12:34:56Z",
  "updated_at": "2026-04-06T12:34:56Z"
}
```

`models_endpoint_available` is held in memory next to the registry entry: rebuilt on startup (initially `true` for all loaded rows; updated by the next list-load call) and refreshed on every save and on every `POST .../{id}/test`. It is *not* persisted as a column.

Test endpoint response:

```json
{ "ok": true, "models_endpoint_available": true }
```

or, on failure:

```json
{ "ok": false, "error": "could not reach backend: dial tcp: i/o timeout" }
```

### 6.3 Key handling on update

The `PUT` body always carries `api_key`, interpreted in one of two ways:

- If the value matches the row's current `masked_key` (e.g. `sk-...wxyz`), the handler treats this as "do not change the key" — the existing ciphertext is kept untouched.
- Otherwise, the handler treats the value as a new plaintext key, encrypts it, and overwrites the ciphertext.

The masked value contains literal `...` characters and is not a valid OpenAI key, so there is no ambiguity between "user pasted the masked value back" and "user pasted a real key that happens to look masked."

The connection test on update uses *the effective key* — i.e., the new plaintext if provided, or the decrypted existing ciphertext if not. The test runs against the *new* `base_url` if it changed.

### 6.4 Validation order on create / update

Fail fast, return the first error:

1. Body parses as JSON → 400 on failure.
2. `name` present, format valid, not reserved → 400.
3. `base_url` parses as `http`/`https` → 400.
4. `api_key` is non-empty (or, on update, resolves to a non-empty effective key) → 400.
5. On create: `name` not already in use → 409 on conflict.
6. On update: `name` not already in use *by a different row* → 409.
7. Connection test (`GET {base_url}/models` with the effective key, 5s timeout):
   - 2xx → pass; `models_endpoint_available = true`.
   - 404 → pass with warning; `models_endpoint_available = false`. Response includes `models_endpoint_available: false` so the UI can warn.
   - 401 → 400 to the admin: "authentication failed against backend (HTTP 401)".
   - Any other 4xx/5xx → 400 with the upstream status and message.
   - Network error / timeout → 400 with "could not reach backend: <err>".
8. Encrypt key (if changed), upsert row, register/re-register in registry.
9. Return the masked-shape row.

If step 8 fails after step 7 succeeded, the registry is *not* mutated and the response is 500. The DB and the registry stay consistent.

## 7. The `internal/llm/openai` client

### 7.1 Package layout

```
internal/llm/openai/
    client.go         — Client struct, NewClient, CreateMessage, StreamMessage,
                        ValidateOptions, ValidateModelName, ListModels,
                        InvalidateModelCache
    client_test.go    — table-driven tests against an httptest.Server
    hints.go          — OpenAIHints struct
    wire.go           — request/response JSON structs
    wire_test.go      — golden-fixture round-trip tests
    translate.go      — MessageRequest <-> wire types
    translate_test.go — table-driven translator tests
    stream.go         — SSE stream parser
    stream_test.go    — table-driven stream parser tests
    loader.go         — startup loader: read table, decrypt, register
    loader_test.go    — startup loader tests
    testdata/         — captured wire-format fixtures
```

### 7.2 `Client` struct

```go
type Client struct {
    httpClient *http.Client
    baseURL    string
    apiKey     string
    modelCache *llm.ModelCache
}

func NewClient(baseURL, apiKey string, opts ...Option) *Client
```

`NewClient` strips trailing slashes from `baseURL`, validates that it parses as `http`/`https`, and constructs the cache. `Option` is the standard functional-options pattern (`WithHTTPClient`, `WithTimeout`). Production code uses defaults; tests inject mocks.

### 7.3 `OpenAIHints`

```go
type OpenAIHints struct {
    Temperature     *float64 // [0, 2]
    TopP            *float64 // [0, 1]
    ReasoningEffort *string  // "low" | "medium" | "high"
    MaxOutputTokens *int     // > 0; overrides MessageRequest.MaxTokens if set
}
```

`ValidateOptions` parses `map[string]any` from policy YAML into this struct, returns a single error listing all problems. Unknown keys are an error.

### 7.4 `CreateMessage`

1. Translate `MessageRequest` → wire request via `translate.BuildChatCompletionRequest` (see §7.6).
2. POST to `{baseURL}/chat/completions` with `Authorization: Bearer <key>`, `Content-Type: application/json`.
3. On non-2xx: parse `{error: {message, type, code}}` if present, return a wrapped error. Status mapping:
   - 401 → wrapped with the unauthorized sentinel matching the existing pattern in `internal/llm` (or a new package-local sentinel — match whatever Anthropic/Google already do; resolve at implementation time).
   - 429 → wrapped with the rate-limit sentinel matching the existing pattern.
   - Other 4xx/5xx → wrapped, no sentinel.
   - Network/timeout → wrapped; if context was cancelled, return `ctx.Err()`.
4. On 2xx: translate the wire response to `MessageResponse` via `translate.ParseChatCompletionResponse` (see §7.6).

### 7.5 `StreamMessage`

Same translation, with `stream: true` and `stream_options: {include_usage: true}`. The response is parsed as SSE: lines beginning with `data: `, terminated by `data: [DONE]`.

- Text deltas → `MessageChunk{Text: &delta}` emitted as they arrive.
- Tool-call deltas are buffered. OpenAI streams tool calls in pieces — first delta has `index`, `id`, `function.name`; subsequent deltas have `function.arguments` fragments. The parser maintains `map[int]*partialToolCall` and emits a complete `MessageChunk{ToolCall: ...}` when the stream's `finish_reason` arrives. **Tool calls are not emitted incrementally.**
- The final chunk emits `MessageChunk{StopReason: &sr, Usage: &u}` and closes the channel.
- Any parse error or HTTP error emits `MessageChunk{Err: err}` as the final chunk.
- Context cancellation: the goroutine selects on `ctx.Done()` and emits `MessageChunk{Err: ctx.Err()}` before closing.
- The channel is *always* closed exactly once via `defer close(ch)`.

### 7.6 Translation rules

**`BuildChatCompletionRequest` (`MessageRequest` → wire):**

- System prompt → first message with `role: "system"`. Empty system prompt → omitted.
- `RoleAssistant` turn:
  - Text blocks concatenated with `\n\n` into the message `content` (single string).
  - Tool call blocks → `tool_calls` array; if there are tool calls and no text, `content: null`.
  - Thinking blocks dropped (audit-only; never sent back to providers).
- `RoleUser` turn with text only → one user message with concatenated `content`.
- `RoleUser` turn with tool results → **N separate `role: "tool"` messages**, one per tool result, each with its own `tool_call_id` and `content`. `IsError: true` is encoded by prefixing the content with `[error] `.
- `RoleUser` turn with both text and tool results → tool messages first, then a user message with the concatenated text. Best-effort; not relied on by the agent runtime.
- Each `ToolDefinition` → `{type: "function", function: {name, description, parameters: <inputSchema>}}`. Tool names are passed through unchanged (Gleipnir's `internal/llm/toolname.go` already normalizes).
- `MaxTokens` → `max_tokens` for chat models; `max_completion_tokens` for o-series. Heuristic: model name starts with `o` (`o1`, `o3`, `o4-mini`) or contains `reasoning`. This heuristic is contained to one function in `translate.go`.
- `Hints` (when `*OpenAIHints`) → `temperature`, `top_p`, `reasoning_effort`, `max_completion_tokens`. `reasoning_effort` is sent only for o-series models. Unknown `Hints` types are silently ignored.

**`ParseChatCompletionResponse` (wire → `MessageResponse`):**

- `choices[0].message.content` (string) → one `TextBlock` if non-empty.
- `choices[0].message.tool_calls[]` → `ToolCallBlock`s with the wire `id` passed through verbatim, `name = function.name`, `Input = json.RawMessage(function.arguments)`. The arguments string is validated to parse as JSON; if it does not, return an error.
- `finish_reason`:
  - `stop` → `StopReasonEndTurn`
  - `tool_calls` → `StopReasonToolUse`
  - `length` → `StopReasonMaxTokens`
  - `content_filter` or any other value → `StopReasonError`
- `usage.prompt_tokens` → `InputTokens`
- `usage.completion_tokens` → `OutputTokens`
- `usage.completion_tokens_details.reasoning_tokens` → `ThinkingTokens` (zero for chat models, non-zero for o-series; absent fields → 0, no error)
- `Thinking` is always `nil`.

### 7.7 `ListModels`, `ValidateModelName`, `InvalidateModelCache`

- `ListModels` consults `modelCache` first. On miss, `GET {baseURL}/models` with `Authorization: Bearer <key>`. Each entry in `data[]` becomes `ModelInfo{Name: id, DisplayName: id}`. Cache for the lifetime of the process or until `InvalidateModelCache`.
- `ValidateModelName(ctx, modelName)` calls `ListModels` and checks membership. If the backend returned 404 on `/models` (compat backend without that endpoint), `ValidateModelName` accepts any non-empty string with no error — we cannot validate against an unknown universe.
- `InvalidateModelCache` clears the cache; the next call refetches.

### 7.8 Capability and approval semantics

The client never decides what tools the agent can call, never intercepts approvals, never narrows schemas. It receives a `MessageRequest` with `Tools` already filtered, schemas already narrowed, and emits `ToolCallBlock`s without judgment. ADR-001, ADR-008, and ADR-017 enforcement happens above this layer.

## 8. Admin UI

### 8.1 Section placement

The new UI lives in a new section on the existing admin LLM Providers page (built in #529), below the existing Anthropic and Google rows. Heading: **"OpenAI-compatible providers"**. Brief description, table of instances, "Add provider" button.

### 8.2 Table columns

| Column | Content |
|---|---|
| Name | Instance name (`openai`, `ollama-local`, etc.) |
| Base URL | Configured `base_url`, displayed verbatim |
| API Key | Masked key (`sk-...wxyz`), monospace |
| Models | Either a count (`47 models`) or a "models endpoint unavailable" badge |
| Updated | Relative time since `updated_at` |
| Actions | Test, Edit, Delete |

Empty state: friendly panel with text "No OpenAI-compatible providers configured. Add one to use OpenAI, Ollama, vLLM, or any compatible backend." and a primary "Add provider" button.

### 8.3 Add / edit modal

One component, two modes. Fields, in order:

- **Name** — text input. Inline validation for the regex and reserved-name rules. Help text: "Lowercase letters, numbers, hyphens, and underscores. This is what policies will reference."
- **Base URL** — text input. Placeholder `https://api.openai.com/v1`. Help text: "The OpenAI Chat Completions endpoint. For OpenAI itself, use `https://api.openai.com/v1`. For Ollama, use `http://your-host:11434/v1`."
- **API Key** — password input. In edit mode, pre-filled with the masked value. Help text in edit mode: "Leave unchanged to keep the current key. Paste a new key to replace it."

A single quick-preset chip above the form: **OpenAI**. Clicking it fills `name` (if blank) with `openai`, fills `base_url` with `https://api.openai.com/v1`, and leaves the API key empty for the user to paste. Help text under the chip: "Quick-fill the OpenAI defaults. Edit any field after applying."

### 8.4 Submit flow

- Form submit fires `POST` (create) or `PUT` (edit). The connection test runs server-side as part of the save (§6.4).
- The UI shows a spinner with "Testing connection..." while waiting.
- On success: modal closes, table refreshes, success toast. If the response carries `models_endpoint_available: false`, the toast is a warning variant: "Saved. Note: this backend doesn't expose `/models` — you'll need to type model names manually in policies."
- On 4xx: modal stays open, error message inline at the top of the form. User input is preserved.
- On 5xx: same, with a generic "Save failed, please try again" wrapper.

### 8.5 Test and delete

- **Test button** on existing rows fires `POST .../{id}/test`, shows a spinner, then either a green check or a red error tooltip. Updates the row's `models_endpoint_available` badge in place.
- **Delete button** opens a confirmation dialog with explicit consequences: "Deleting this provider will not stop any runs currently in progress, but new runs that reference `<name>` will fail. Policies referencing this provider will need to be updated manually." Confirm fires `DELETE`, table refreshes, toast on success.

### 8.6 UI conventions

- No emojis anywhere in this UI (per project rules). Status indicators use text labels and color, not icons-with-emoji.
- CSS Modules only; 4px spacing scale; existing design tokens. No inline styles, no off-grid spacing.
- Each new component (section, table row, empty state, modal in create mode, modal in edit mode, modal in error state, delete confirm) gets a Storybook story using existing MSW mock handlers.
- Loading and error states use the existing TanStack Query patterns (skeleton loaders, error toasts) already used elsewhere in the admin section.

### 8.7 Policy editor

The provider dropdown in the policy editor reads registered providers from the same `ProviderRegistry`. Provided the existing implementation reads from the registry rather than a hardcoded list, no changes are required and OpenAI-compat instances appear automatically. **Known unknown — verify during implementation.** If the dropdown is currently hardcoded, the small fix is in scope and is expected to be a one-line change.

### 8.8 Out of scope for v1

- No "import config from JSON / YAML" feature.
- No bulk add (one provider at a time).
- No per-provider model enable/disable on this section (the existing model-disable mechanism in `internal/admin` continues to work uniformly).
- No per-provider traffic, cost, or usage stats.

## 9. Testing strategy

### 9.1 Wire-type round-trip tests (`internal/llm/openai/wire_test.go`)

Golden-fixture tests that take known-good JSON payloads and round-trip them through the wire structs. Fixtures live in `internal/llm/openai/testdata/`:

- `chat_response_text_only.json`
- `chat_response_with_tool_calls.json`
- `chat_response_parallel_tool_calls.json`
- `chat_response_o_series_with_reasoning_tokens.json`
- `chat_response_finish_length.json`
- `chat_response_finish_content_filter.json`
- `chat_response_error_401.json`, `chat_response_error_429.json`, `chat_response_error_500.json`
- `models_response.json`
- `stream_chunks_text.txt`, `stream_chunks_with_tool_calls.txt`, `stream_chunks_with_usage.txt`

Fixtures are committed sample payloads pulled from real responses (sensitive fields scrubbed). They serve as a regression suite.

### 9.2 Translator tests (`internal/llm/openai/translate_test.go`)

Table-driven. Required cases for `BuildChatCompletionRequest`:

- Empty / non-empty system prompt
- User turn with single text block / multiple text blocks
- Assistant turn with text only / tool calls only / both
- Assistant turn with thinking blocks → blocks dropped
- User turn with single tool result / multiple tool results / tool result with `IsError: true`
- User turn mixing text and tool results
- Tool definitions with various JSON schemas
- `MaxTokens` with non-o-series → `max_tokens`
- `MaxTokens` with o-series (e.g. `o3-mini`) → `max_completion_tokens`
- `OpenAIHints` various combinations including nil
- `ReasoningEffort` set on non-o-series → field omitted
- Unknown `Hints` type → silently ignored, no panic

For `ParseChatCompletionResponse`:

- Each `finish_reason` mapping
- Tool call with malformed `function.arguments` JSON → error
- Missing `usage.completion_tokens_details` → `ThinkingTokens = 0`, no error
- Reasoning tokens present → recorded

### 9.3 Stream parser tests (`internal/llm/openai/stream_test.go`)

Table-driven. Required cases:

- Text-only stream
- Stream with tool calls — verify tool calls are emitted only when complete, not partially
- Stream with `[DONE]` terminator → channel closes cleanly
- Stream that ends without `[DONE]` → channel closes with an error chunk
- Stream interrupted by context cancellation → final chunk has `Err == ctx.Err()`
- Stream with malformed JSON in a `data:` line → final chunk has an error
- Stream with split tool-call deltas across multiple chunks → one complete tool-call chunk emitted
- Stream with usage chunk at the end → `Usage` populated on the final stop chunk
- Channel-closed-exactly-once invariant: a test that asserts no panic on a second receive

### 9.4 Client integration tests (`internal/llm/openai/client_test.go`)

Use `httptest.Server` to simulate OpenAI. Each test starts a server with a specific handler, constructs a `Client` pointed at it, exercises a method, asserts the result. Required cases:

- `CreateMessage` happy path / with tools
- `CreateMessage` 401 / 429 / 500 / network error / context cancellation
- `StreamMessage` happy path / with cancellation
- `ListModels` happy path / cached (verified via request counter) / 404 → empty slice
- `InvalidateModelCache` → next `ListModels` hits the server
- `ValidateModelName` known / unknown / against backend with `/models` 404
- `ValidateOptions` valid / each invalid case
- `Authorization: Bearer <key>` header present on every request

### 9.5 Admin handler tests (`internal/admin/openai_compat_handler_test.go`)

Table-driven HTTP tests using `httptest.NewRecorder`, fake `AdminQuerier`, fake registry:

- `POST` happy path → 201, row created, registry called
- `POST` with reserved name → 400, no DB write
- `POST` with invalid name format / malformed `base_url` / empty `api_key` / duplicate name (409)
- `POST` with connection test 401 / 404 → 400 / 200 with warning
- `POST` with connection test timeout / network error → 400
- `POST` with DB write failure after successful test → 500, registry NOT mutated
- `PUT` with masked key in body → ciphertext unchanged
- `PUT` with new plaintext key → ciphertext re-encrypted
- `PUT` changing `name` → unregister old, register new
- `PUT` changing `name` to one already in use by another row → 409
- `DELETE` happy path → 204, registry unregistered
- `DELETE` non-existent → 404
- `GET` list / by id → masked keys, never plaintext
- `POST .../{id}/test` happy path / against an unreachable backend → no state mutation
- All endpoints reject non-admin → 403

### 9.6 Registry-loader tests (`internal/llm/openai/loader_test.go`)

- Startup with no rows → no openai providers registered
- Startup with N rows → N providers registered
- Startup with one corrupt-ciphertext row → that row skipped, others register, no abort
- Startup with two same-name rows (defensive) → second registration logs warning

### 9.7 End-to-end smoke test (build-tagged `//go:build e2e`)

Optional, gated behind a build tag and `OPENAI_API_KEY`. Hits real OpenAI with a trivial completion to verify the wire format against the live service. Not required to pass for the spec to be complete.

### 9.8 Out of scope for testing

- Agent runtime behavior with an OpenAI provider — already covered by existing `internal/agent` tests against the generic `LLMClient` interface.
- Frontend UI — covered by standard Vitest and Storybook patterns established for the rest of the admin section.
- Encryption / decryption — already covered in `internal/admin/crypto_test.go`.

### 9.9 Coverage target

Every code path in `internal/llm/openai/*.go` that isn't a one-line getter or struct field access. The translator and stream parser are exhaustively table-tested.

## 10. ADR-032 (introduced by this spec)

**Title:** Admin-managed OpenAI-compatible LLM provider instances

**Status:** Proposed (will be marked Accepted when this spec's implementation lands).

**Context.** Gleipnir's existing LLM provider model (ADR-026) supports two providers — Anthropic and Google — each backed by a vendor SDK and configured via a single fixed `<provider>_api_key` row in `system_settings`. The provider list is a static `knownProviders` slice baked in at startup. This works for two cloud providers with stable, vendor-specific APIs, but it does not extend to:

1. Adding OpenAI as a third first-class provider (issue #533).
2. Letting operators point Gleipnir at OpenAI-compatible backends (Ollama, vLLM, OpenRouter, LM Studio, Together, Groq, Azure-via-compat) which all share the OpenAI Chat Completions wire format but live at arbitrary `base_url`s with arbitrary credentials.
3. Allowing administrators to add or change LLM endpoints at runtime without redeploying.

**Decision.** Introduce a second provider mechanism that coexists with the existing SDK-backed mechanism:

- **SDK providers (`anthropic`, `google`)** remain exactly as today. One row per provider in `system_settings`. Static `knownProviders` slice. Vendor SDKs. They are inherently special — vendor-specific features (prompt caching, signed thinking blocks, citations, structured outputs) justify per-provider client code.
- **OpenAI-compatible provider instances** are admin-managed, persisted in a new `openai_compat_providers` table, and registered into the existing `ProviderRegistry` at startup and on every admin mutation. Each row is an *instance* of one shared client implementation: a single hand-rolled `*openai.Client` constructed with the row's `base_url` and decrypted `api_key`. The same client serves OpenAI itself (`base_url = https://api.openai.com/v1`) and any compatible third-party backend.

**Why hand-rolled, not the official OpenAI Go SDK.** See §3.4.

**Why Chat Completions only, not the Responses API.** The Responses API is OpenAI-only (compat backends don't implement it). Surfacing reasoning *content* from o-series models requires it, but reasoning *token counts* are available on Chat Completions and are what `TokenUsage.ThinkingTokens` records. Standard chat models have no hidden reasoning, so nothing is lost there. Adding Responses API support later is a contained change inside the openai package and does not require an interface change.

**Why two mechanisms instead of unifying everything in one table.** Migrating Anthropic and Google into the new table was rejected because they are *legitimately* special: vendor SDKs and features that don't fit a uniform shape. Forcing them in would either lose features or require a `type` discriminator with per-type code paths that recreate the existing per-provider packages. The two-mechanism approach is honest about the underlying difference.

**Why the reserved-name rule.** Without it, an admin could create an `openai_compat_providers` row named `anthropic` and silently shadow the SDK-backed Anthropic provider in the registry. Enforced once in the admin handler.

**Why API keys are encrypted at rest.** Reuses the existing `internal/admin/crypto.go` and `GLEIPNIR_SECRET_KEY` infrastructure used for Anthropic and Google. No new key-management story.

**Why deletion is destructive without policy checks.** A policy referencing an unknown provider already fails at run-start with a clear error. Adding a "this provider is referenced by N policies" check is desirable but not required for v1; it can be added without changing this ADR. In-flight runs that already hold a client reference complete their current API call and only fail when their next run starts and tries to look up the provider in the registry. Deleting a provider does not interrupt running agents.

**Why connection-test-on-save (with a 404 escape hatch).** Catching authentication failures, typos, and unreachable backends in the admin UI at save time rather than in a policy author's failed run hours later is a much better operator experience. The 404 escape hatch exists because some compat backends do not implement `/v1/models`; they should still be usable, with the trade-off that model-name autocomplete is unavailable for those instances.

**Consequences.**

- New table `openai_compat_providers`. Migration is additive.
- New admin endpoints under `/api/v1/admin/openai-providers`, admin-role gated.
- New section on the existing admin LLM Providers page. Anthropic and Google sections unchanged.
- New Go package `internal/llm/openai`, mirroring `internal/llm/anthropic` and `internal/llm/google`.
- Policy YAML unchanged. Policies continue to say `provider: <name>`.
- Two parallel provider mechanisms exist after this change. Future LLM providers that *also* speak OpenAI Chat Completions require zero new code (just an admin-created instance). Future LLM providers that need a vendor SDK require a new package alongside `anthropic` and `google` and an entry in `knownProviders`.

**Supersedes / amends.** Builds on ADR-026 (Model-Agnostic Design); does not supersede it. Adds a second registration mechanism alongside the existing static one. ADR-001 (hard capability enforcement) is unchanged — the new client never sees policy details; it only receives filtered tool lists.

## 11. Open implementation questions to resolve while coding

These are deliberately deferred to implementation, not the spec, because they have an obvious answer once the existing code is in front of you:

1. **Sentinel error pattern.** Whether `internal/llm` exposes shared sentinels (`ErrUnauthorized`, `ErrRateLimited`) or whether each provider package defines its own. Match whatever Anthropic/Google already do.
2. **Policy editor provider dropdown.** Verify it reads from the live `ProviderRegistry`. If it's hardcoded, fix it (expected to be a one-line change).
3. **Loader location.** Whether `loader.go` lives in `internal/llm/openai` or in `main.go` next to the existing provider wiring. Pick whichever fits the existing patterns better.
