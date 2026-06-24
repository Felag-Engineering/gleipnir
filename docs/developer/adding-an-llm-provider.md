# Adding a New LLM Provider

Gleipnir supports multiple LLM providers through the `llm.LLMClient` interface (ADR-026). The agent runtime is provider-agnostic.

You do **not** implement `LLMClient` directly. Each provider implements the narrower `llm.ProviderWire` seam (wire-level translation) and embeds the shared `*llm.ProviderAdapter`, which owns the common choreography — request-duration/token/error metrics, the metrics defer, and promotion of `CreateMessage`/`StreamMessage` onto the exported client. This is the seam introduced for #506; see `internal/llm/wire.go` and `internal/llm/adapter.go`.

Use the `google` provider as a reference for a clean built-in implementation, or `openaicompat` for the dynamic registration pattern.

## The interface

`LLMClient` (in `internal/llm/interface.go`) is the provider-agnostic interface the agent runtime consumes. `ProviderAdapter` already satisfies it in full:

```go
type LLMClient interface {
    CreateMessage(ctx context.Context, req MessageRequest) (*MessageResponse, error)
    StreamMessage(ctx context.Context, req MessageRequest) (<-chan MessageChunk, error)
    ValidateOptions(options map[string]any) error
    ValidateModelName(ctx context.Context, modelName string) error
    ListModels(ctx context.Context) ([]ModelInfo, error)
    InvalidateModelCache()
}
```

What you actually implement is `llm.ProviderWire` (`internal/llm/wire.go`):

```go
type ProviderWire interface {
    Call(ctx context.Context, req MessageRequest) (*MessageResponse, error)   // synchronous
    Stream(ctx context.Context, req MessageRequest) (<-chan MessageChunk, error) // per-provider state machine
    ListModels(ctx context.Context) ([]ModelInfo, error)
    ValidateModelName(ctx context.Context, modelName string) error
    ValidateOptions(options map[string]any) error
    InvalidateModelCache()
    ClassifyError(err error) string // maps an SDK error to the fixed metrics error_type enum
    ProviderName() string           // the Prometheus provider label
}
```

The adapter calls `Call`/`Stream` and wraps them with the metrics defer (using `ClassifyError` + `ProviderName`); the agent never sees the wire directly. Key data types are defined in `interface.go`: `MessageRequest`, `MessageResponse`, `MessageChunk`, `ConversationTurn`, `ToolDefinition`, `ModelInfo`.

## Checklist

### 1. Create the provider package

**Directory:** `internal/llm/yourprovider/`

Create at minimum:

- **`client.go`** — defines an unexported `wire` type that implements `llm.ProviderWire`, plus an exported client struct that embeds `*llm.ProviderAdapter` and is constructed via `llm.NewAdapter(w)`. Include a compile-time assertion: `var _ llm.LLMClient = (*YourClient)(nil)` (the embedded adapter satisfies it). The constructor takes an API key and any provider-specific config. See `internal/llm/google/client.go` for the exact shape — the wire owns `Call`/`Stream`/translation; the client struct keeps thin forwarders only where needed.
- **`models.go`** — curated model list or dynamic fetch with `llm.ModelCache` (defined in `internal/llm/model_cache.go`).
- **`hints.go`** — provider-specific policy options (if your provider supports options like thinking budgets, grounding, etc.). Parse from `map[string]any` in `ValidateOptions()`.

Optional:
- **`schema.go` / `translate.go`** — separate file for request/response/tool-schema translation if the mapping is complex (Google uses `schema.go`).
- **`stream.go`** — the streaming state machine if it needs significant code (the per-provider accumulate-and-emit loop lives here).

### 2. Handle tool name sanitization

**File:** `internal/llm/toolname.go`

MCP tool names use dots (e.g., `todoist.get_tasks`), but providers have different naming rules. Call `llm.BuildNameMapping(req.Tools, allowedExtra)` inside your wire's `Call`/`Stream` implementation, where `allowedExtra` is provider-specific (e.g., `"-"` for Anthropic/OpenAI, `""` for Google).

Sanitize tool names in outgoing requests and reverse-map them in incoming responses.

### 3. Register as a known provider

For a built-in provider (compiled into the binary), two files change:

1. **`internal/llm/factory/factory.go`** — add a `case "yourprovider":` to `NewClientForProvider`'s switch that constructs your client. This is the single touch point that maps a provider-name string to a concrete client. The factory lives in its own sub-package to avoid the import cycle caused by provider packages importing `internal/llm`.

   ```go
   case "yourprovider":
       return yourprovider.NewClient(apiKey), nil
   ```

2. **`main.go`** — add your provider name to the `knownProviders` slice (`var knownProviders = []string{...}`). `main.go` calls `configureProvider`, which delegates to `llmfactory.NewClientForProvider`. If your provider previously used an env-based API key, also add a startup warning so a stale `YOURPROVIDER_API_KEY` env var is flagged as ignored.

(OpenAI-compatible backends are registered dynamically at runtime via the admin UI rather than the factory switch — see `internal/llm/openaicompat/`.)

### 4. Write tests

Create `*_test.go` files in your provider package covering:
- Request translation (your format to provider API format)
- Response translation (API response to `llm.MessageResponse`)
- Tool name sanitization round-trip
- Error handling (auth failures, rate limits, malformed responses)
- Model listing and validation
- Options/hints validation

Use test fixtures in a `testdata/` directory for canned API responses (see `internal/llm/openai/testdata/` for examples).

Add your wire to the cross-wire contract suite in **`internal/llm/contract/`**. It is table-driven over every real wire and asserts continuity-state round-trip, tool-name round-trip, stop-reason normalization, and usage extraction uniformly. It lives outside `internal/llm` to avoid the provider-import cycle (same reason as `factory/`).

## What you don't need to change

The following are already provider-agnostic and require no modifications:

- **API routes** — `/api/v1/models`, `/api/v1/admin/providers/*` work for all providers via the `ProviderRegistry`
- **Admin UI** — provider key management and model enablement are generic
- **Database schema** — `system_settings` stores encrypted API keys by convention (`yourprovider_api_key`), `model_settings` tracks enabled models. No migrations needed.
- **Agent runtime** — `BoundAgent` calls `LLMClient` methods without knowing the provider
- **Encryption** — API keys are encrypted/decrypted transparently via `internal/admin/crypto.go`

## Reference implementations

| Provider | Package | Notes |
|----------|---------|-------|
| Anthropic | `internal/llm/anthropic/` | Curated models, Anthropic SDK |
| Google | `internal/llm/google/` | Curated models, schema translation for Gemini |
| OpenAI | `internal/llm/openai/` | Curated models, Chat Completions API |
| OpenAI-compatible | `internal/llm/openaicompat/` | Dynamic registration, admin-configured base URL |
