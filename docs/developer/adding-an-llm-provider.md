# Adding a New LLM Provider

Gleipnir supports multiple LLM providers through the `llm.LLMClient` interface (ADR-026). All providers implement the same interface; the agent runtime is provider-agnostic.

Use the `google` provider as a reference for a clean built-in implementation, or `openaicompat` for the dynamic registration pattern.

## The interface

Every provider must implement all methods in `internal/llm/interface.go`:

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

Key data types are defined in the same file: `MessageRequest`, `MessageResponse`, `MessageChunk`, `ConversationTurn`, `ToolDefinition`, `ModelInfo`.

## Checklist

### 1. Create the provider package

**Directory:** `internal/llm/yourprovider/`

Create at minimum:

- **`client.go`** — the `LLMClient` implementation. Include a compile-time assertion: `var _ llm.LLMClient = (*Client)(nil)`. Constructor takes an API key and any provider-specific config.
- **`models.go`** — curated model list or dynamic fetch with `llm.ModelCache` (defined in `internal/llm/model_cache.go`).
- **`hints.go`** — provider-specific policy options (if your provider supports options like thinking budgets, grounding, etc.). Parse from `map[string]any` in `ValidateOptions()`.

Optional:
- **`translate.go`** — separate file for request/response translation if the mapping is complex.
- **`stream.go`** — streaming implementation if it needs significant code.

### 2. Handle tool name sanitization

**File:** `internal/llm/toolname.go`

MCP tool names use dots (e.g., `todoist.get_tasks`), but providers have different naming rules. Call `llm.BuildNameMapping(req.Tools, allowedExtra)` in your `CreateMessage` implementation, where `allowedExtra` is provider-specific (e.g., `"-"` for Anthropic/OpenAI, `""` for Google).

Sanitize tool names in outgoing requests and reverse-map them in incoming responses.

### 3. Register as a known provider

**File:** `main.go`

Three changes:
1. Add your provider name to the `knownProviders` slice
2. Add a case to the `configureProvider` switch to construct your client
3. Add an env var warning if your provider previously used env-based API keys

```go
case "yourprovider":
    client = yourproviderllm.NewClient(apiKey)
```

### 4. Write tests

Create `*_test.go` files in your provider package covering:
- Request translation (your format to provider API format)
- Response translation (API response to `llm.MessageResponse`)
- Tool name sanitization round-trip
- Error handling (auth failures, rate limits, malformed responses)
- Model listing and validation
- Options/hints validation

Use test fixtures in a `testdata/` directory for canned API responses (see `internal/llm/openai/testdata/` for examples).

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
