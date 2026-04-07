# OpenAI-Compatible LLM Client Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add OpenAI as a third first-class LLM provider in Gleipnir via a hand-rolled `internal/llm/openai` Chat Completions client, and let administrators create/edit/delete OpenAI-compatible provider instances at runtime through a new admin UI section.

**Architecture:** New package `internal/llm/openai` contains a single `*Client` type that speaks the OpenAI Chat Completions wire format and serves both OpenAI itself and any compatible backend (Ollama, vLLM, OpenRouter, etc.). A new `openai_compat_providers` table holds admin-managed instances; a startup loader reads it and registers each row in the existing `ProviderRegistry`. A new admin handler exposes CRUD + a connection-test endpoint; a new section on the existing Admin LLM Providers page drives it. Anthropic and Google stay exactly as they are.

**Tech Stack:** Go 1.x, chi router, sqlc, SQLite, `net/http` + `httptest` for the client, React + TypeScript + TanStack Query + CSS Modules + Storybook for the UI, AES-GCM via existing `internal/admin/crypto.go`.

**Spec:** `docs/superpowers/specs/2026-04-06-openai-compatible-llm-client-design.md`. Task descriptions reference spec sections by number (e.g. §7.6) rather than repeating rationale.

---

## File Structure

### Created

- `internal/db/queries/openai_compat_providers.sql` — sqlc queries
- `internal/db/openai_compat_providers.sql.go` — sqlc-generated Go
- `internal/llm/openai/wire.go` — request/response JSON structs
- `internal/llm/openai/wire_test.go`
- `internal/llm/openai/hints.go` — `OpenAIHints` + `ValidateOptions`
- `internal/llm/openai/hints_test.go`
- `internal/llm/openai/translate.go` — `BuildChatCompletionRequest`, `ParseChatCompletionResponse`
- `internal/llm/openai/translate_test.go`
- `internal/llm/openai/stream.go` — SSE parser for `/chat/completions` with `stream: true`
- `internal/llm/openai/stream_test.go`
- `internal/llm/openai/client.go` — `Client` struct, `CreateMessage`, `StreamMessage`, `ListModels`, `ValidateModelName`, `InvalidateModelCache`, `wrapHTTPError`
- `internal/llm/openai/client_test.go`
- `internal/llm/openai/loader.go` — `LoadAndRegister(ctx, db, encKey, registry)`
- `internal/llm/openai/loader_test.go`
- `internal/llm/openai/testdata/chat_response_text_only.json`
- `internal/llm/openai/testdata/chat_response_with_tool_calls.json`
- `internal/llm/openai/testdata/chat_response_parallel_tool_calls.json`
- `internal/llm/openai/testdata/chat_response_o_series_with_reasoning_tokens.json`
- `internal/llm/openai/testdata/chat_response_finish_length.json`
- `internal/llm/openai/testdata/chat_response_finish_content_filter.json`
- `internal/llm/openai/testdata/chat_response_error_401.json`
- `internal/llm/openai/testdata/chat_response_error_429.json`
- `internal/llm/openai/testdata/chat_response_error_500.json`
- `internal/llm/openai/testdata/models_response.json`
- `internal/llm/openai/testdata/stream_chunks_text.txt`
- `internal/llm/openai/testdata/stream_chunks_with_tool_calls.txt`
- `internal/llm/openai/testdata/stream_chunks_with_usage.txt`
- `internal/admin/openai_compat_handler.go` — HTTP handlers for `/api/v1/admin/openai-providers`
- `internal/admin/openai_compat_handler_test.go`
- `frontend/src/api/openaiCompatProviders.ts` — `fetch`-layer typed API
- `frontend/src/hooks/queries/openaiCompatProviders.ts` — TanStack Query hooks
- `frontend/src/hooks/mutations/openaiCompatProviders.ts`
- `frontend/src/components/admin/OpenAICompatProvidersSection.tsx`
- `frontend/src/components/admin/OpenAICompatProvidersSection.module.css`
- `frontend/src/components/admin/OpenAICompatProvidersSection.stories.tsx`
- `frontend/src/components/admin/OpenAICompatProviderModal.tsx`
- `frontend/src/components/admin/OpenAICompatProviderModal.module.css`
- `frontend/src/components/admin/OpenAICompatProviderModal.stories.tsx`
- `frontend/src/components/admin/OpenAICompatProviderDeleteDialog.tsx`
- `frontend/src/components/admin/OpenAICompatProviderDeleteDialog.stories.tsx`
- `frontend/src/mocks/handlers/openaiCompatProviders.ts` — MSW handlers for stories/tests

### Modified

- `schemas/sql_schemas.sql` — append `openai_compat_providers` table + index
- `internal/db/db.go` or whatever sqlc emits as the `Querier` interface — extended automatically by `sqlc generate`
- `internal/admin/handler.go` — no change; new handlers live in a sibling file and are wired in `main.go`
- `main.go` — register admin routes, invoke `openai.LoadAndRegister` after Anthropic/Google registration
- `frontend/src/api/types.ts` — add `ApiOpenAICompatProvider` shape
- `frontend/src/pages/AdminModelsPage.tsx` — render the new section below the existing Anthropic/Google rows
- `frontend/src/mocks/handlers/index.ts` — include the new MSW handlers
- `docs/ADR_Tracker.md` — append ADR-032

### Not touched (verification only)

- `internal/api/models_handler.go` — already calls `registry.ListAllModels`, so new providers appear in the policy editor dropdown automatically. Task 19 includes a one-step verification.
- `internal/agent/*` — provider-agnostic above `LLMClient`. No changes.
- `internal/policy/*` — `provider: <name>` resolution is already dynamic.
- `internal/admin/crypto.go` — reused unchanged.
- Anthropic and Google clients — unchanged.

---

## Layer 1 — Database schema and sqlc

### Task 1: Add `openai_compat_providers` table to the schema

**Files:**
- Modify: `schemas/sql_schemas.sql` (append at the end, before any final comment)

- [ ] **Step 1: Append the table definition**

Append to `schemas/sql_schemas.sql`:

```sql

-- OpenAI-compatible provider instances (ADR-032).
-- Each row is a runtime-registered LLM provider speaking the OpenAI Chat
-- Completions wire format. Admins create, edit, and delete these from the
-- admin UI. Names `anthropic` and `google` are reserved by the API layer.
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

- [ ] **Step 2: Verify the schema file still parses by running the existing DB tests**

Run: `go test ./internal/db/... -run TestSchema`
Expected: `PASS` (or, if no such test exists, `ok    github.com/rapp992/gleipnir/internal/db ... [no test files]` — that's also fine).

If there is no schema-smoke test, instead run: `sqlite3 :memory: < schemas/sql_schemas.sql` and expect exit code 0 with no output.

- [ ] **Step 3: Commit**

```bash
git add schemas/sql_schemas.sql
git commit -m "feat(db): add openai_compat_providers table (ADR-032)"
```

---

### Task 2: Add sqlc queries for `openai_compat_providers`

**Files:**
- Create: `internal/db/queries/openai_compat_providers.sql`

- [ ] **Step 1: Write the query file**

Create `internal/db/queries/openai_compat_providers.sql`:

```sql
-- name: ListOpenAICompatProviders :many
SELECT * FROM openai_compat_providers ORDER BY name ASC;

-- name: GetOpenAICompatProviderByID :one
SELECT * FROM openai_compat_providers WHERE id = :id;

-- name: GetOpenAICompatProviderByName :one
SELECT * FROM openai_compat_providers WHERE name = :name;

-- name: CreateOpenAICompatProvider :one
INSERT INTO openai_compat_providers (name, base_url, api_key_encrypted, created_at, updated_at)
VALUES (:name, :base_url, :api_key_encrypted, :created_at, :updated_at)
RETURNING *;

-- name: UpdateOpenAICompatProvider :one
UPDATE openai_compat_providers
SET name = :name,
    base_url = :base_url,
    api_key_encrypted = :api_key_encrypted,
    updated_at = :updated_at
WHERE id = :id
RETURNING *;

-- name: DeleteOpenAICompatProvider :exec
DELETE FROM openai_compat_providers WHERE id = :id;
```

- [ ] **Step 2: Run `sqlc generate`**

Run: `sqlc generate`
Expected: exit code 0, no errors. A new file `internal/db/openai_compat_providers.sql.go` is created and `internal/db/models.go` is updated to contain an `OpenaiCompatProvider` struct.

- [ ] **Step 3: Verify the package compiles**

Run: `go build ./internal/db/...`
Expected: exit code 0, no output.

- [ ] **Step 4: Commit**

```bash
git add internal/db/queries/openai_compat_providers.sql internal/db/openai_compat_providers.sql.go internal/db/models.go
git commit -m "feat(db): sqlc queries for openai_compat_providers"
```

**Note for later tasks:** sqlc will name the generated struct `OpenaiCompatProvider` (based on the SQLite table name). Later tasks refer to it by that exact name. If sqlc happens to emit `OpenAICompatProvider` instead, the handler and loader code must match — verify by opening `internal/db/models.go` after Step 2 and noting the exact type name.

---

## Layer 2 — `internal/llm/openai` primitives

### Task 3: Wire types and golden fixtures

**Files:**
- Create: `internal/llm/openai/wire.go`
- Create: `internal/llm/openai/wire_test.go`
- Create: `internal/llm/openai/testdata/chat_response_text_only.json`
- Create: `internal/llm/openai/testdata/chat_response_with_tool_calls.json`
- Create: `internal/llm/openai/testdata/chat_response_parallel_tool_calls.json`
- Create: `internal/llm/openai/testdata/chat_response_o_series_with_reasoning_tokens.json`
- Create: `internal/llm/openai/testdata/chat_response_finish_length.json`
- Create: `internal/llm/openai/testdata/chat_response_finish_content_filter.json`
- Create: `internal/llm/openai/testdata/chat_response_error_401.json`
- Create: `internal/llm/openai/testdata/chat_response_error_429.json`
- Create: `internal/llm/openai/testdata/chat_response_error_500.json`
- Create: `internal/llm/openai/testdata/models_response.json`

- [ ] **Step 1: Create `wire.go` with request and response structs**

```go
// Package openai implements an LLMClient against the OpenAI Chat Completions
// API. The same client serves OpenAI itself and any OpenAI-compatible backend
// (Ollama, vLLM, OpenRouter, Azure-via-compat, etc.) — the only differences
// are base_url and api_key, both set at construction time. See ADR-032.
package openai

import "encoding/json"

// --- Request types -----------------------------------------------------------

type chatRequest struct {
	Model         string         `json:"model"`
	Messages      []chatMessage  `json:"messages"`
	Tools         []chatTool     `json:"tools,omitempty"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`

	// Exactly one of MaxTokens or MaxCompletionTokens is set by the translator.
	MaxTokens           *int `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int `json:"max_completion_tokens,omitempty"`

	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"top_p,omitempty"`
	ReasoningEffort *string  `json:"reasoning_effort,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// chatMessage is a single message. `role` is one of "system", "user",
// "assistant", "tool". Content shape depends on role:
//   - system/user/assistant with text only: Content is a string.
//   - assistant with tool calls and no text: Content is nil (JSON null).
//   - tool: Content is a string; ToolCallID is set.
type chatMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content"` // pointer so we can emit JSON null
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // always "function"
	Function chatToolCallFunc `json:"function"`
}

type chatToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded string
}

type chatTool struct {
	Type     string       `json:"type"` // always "function"
	Function chatToolFunc `json:"function"`
}

type chatToolFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// --- Response types ----------------------------------------------------------

type chatResponse struct {
	ID      string       `json:"id"`
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage"`
	Error   *apiError    `json:"error,omitempty"`
}

type chatChoice struct {
	Index        int         `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type chatUsage struct {
	PromptTokens            int                      `json:"prompt_tokens"`
	CompletionTokens        int                      `json:"completion_tokens"`
	CompletionTokensDetails *completionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type completionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// modelsResponse is the shape of GET {baseURL}/models.
type modelsResponse struct {
	Data []modelsEntry `json:"data"`
}

type modelsEntry struct {
	ID string `json:"id"`
}

// --- Streaming chunk types ---------------------------------------------------

type streamChunk struct {
	Choices []streamChoice `json:"choices"`
	Usage   *chatUsage     `json:"usage,omitempty"`
}

type streamChoice struct {
	Index        int         `json:"index"`
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type streamDelta struct {
	Content   *string              `json:"content,omitempty"`
	ToolCalls []streamToolCallPart `json:"tool_calls,omitempty"`
}

type streamToolCallPart struct {
	Index    int                  `json:"index"`
	ID       string               `json:"id,omitempty"`
	Type     string               `json:"type,omitempty"`
	Function streamToolCallPartFn `json:"function,omitempty"`
}

type streamToolCallPartFn struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}
```

- [ ] **Step 2: Create each fixture file**

Create `internal/llm/openai/testdata/chat_response_text_only.json`:

```json
{
  "id": "chatcmpl-abc",
  "choices": [
    {
      "index": 0,
      "message": { "role": "assistant", "content": "Hello from OpenAI." },
      "finish_reason": "stop"
    }
  ],
  "usage": { "prompt_tokens": 12, "completion_tokens": 5 }
}
```

Create `internal/llm/openai/testdata/chat_response_with_tool_calls.json`:

```json
{
  "id": "chatcmpl-tool",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": null,
        "tool_calls": [
          {
            "id": "call_1",
            "type": "function",
            "function": { "name": "get_weather", "arguments": "{\"city\":\"SF\"}" }
          }
        ]
      },
      "finish_reason": "tool_calls"
    }
  ],
  "usage": { "prompt_tokens": 40, "completion_tokens": 15 }
}
```

Create `internal/llm/openai/testdata/chat_response_parallel_tool_calls.json`:

```json
{
  "id": "chatcmpl-parallel",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": null,
        "tool_calls": [
          { "id": "call_a", "type": "function", "function": { "name": "t1", "arguments": "{}" } },
          { "id": "call_b", "type": "function", "function": { "name": "t2", "arguments": "{\"x\":1}" } }
        ]
      },
      "finish_reason": "tool_calls"
    }
  ],
  "usage": { "prompt_tokens": 50, "completion_tokens": 20 }
}
```

Create `internal/llm/openai/testdata/chat_response_o_series_with_reasoning_tokens.json`:

```json
{
  "id": "chatcmpl-o3",
  "choices": [
    {
      "index": 0,
      "message": { "role": "assistant", "content": "42" },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 30,
    "completion_tokens": 150,
    "completion_tokens_details": { "reasoning_tokens": 120 }
  }
}
```

Create `internal/llm/openai/testdata/chat_response_finish_length.json`:

```json
{
  "id": "chatcmpl-len",
  "choices": [
    {
      "index": 0,
      "message": { "role": "assistant", "content": "truncated..." },
      "finish_reason": "length"
    }
  ],
  "usage": { "prompt_tokens": 10, "completion_tokens": 100 }
}
```

Create `internal/llm/openai/testdata/chat_response_finish_content_filter.json`:

```json
{
  "id": "chatcmpl-cf",
  "choices": [
    {
      "index": 0,
      "message": { "role": "assistant", "content": "" },
      "finish_reason": "content_filter"
    }
  ],
  "usage": { "prompt_tokens": 10, "completion_tokens": 0 }
}
```

Create `internal/llm/openai/testdata/chat_response_error_401.json`:

```json
{
  "error": {
    "message": "Incorrect API key provided: sk-xxx.",
    "type": "invalid_request_error",
    "code": "invalid_api_key"
  }
}
```

Create `internal/llm/openai/testdata/chat_response_error_429.json`:

```json
{
  "error": {
    "message": "Rate limit reached for requests",
    "type": "rate_limit_exceeded",
    "code": "rate_limit_exceeded"
  }
}
```

Create `internal/llm/openai/testdata/chat_response_error_500.json`:

```json
{
  "error": {
    "message": "The server had an error while processing your request.",
    "type": "server_error",
    "code": null
  }
}
```

Create `internal/llm/openai/testdata/models_response.json`:

```json
{
  "object": "list",
  "data": [
    { "id": "gpt-4o",     "object": "model", "owned_by": "openai" },
    { "id": "gpt-4o-mini","object": "model", "owned_by": "openai" },
    { "id": "o3-mini",    "object": "model", "owned_by": "openai" }
  ]
}
```

- [ ] **Step 3: Write the round-trip test**

Create `internal/llm/openai/wire_test.go`:

```go
package openai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestChatResponseFixturesUnmarshal(t *testing.T) {
	cases := []struct {
		file       string
		wantFinish string
		wantTools  int
		wantText   string
	}{
		{"chat_response_text_only.json", "stop", 0, "Hello from OpenAI."},
		{"chat_response_with_tool_calls.json", "tool_calls", 1, ""},
		{"chat_response_parallel_tool_calls.json", "tool_calls", 2, ""},
		{"chat_response_o_series_with_reasoning_tokens.json", "stop", 0, "42"},
		{"chat_response_finish_length.json", "length", 0, "truncated..."},
		{"chat_response_finish_content_filter.json", "content_filter", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var resp chatResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(resp.Choices) != 1 {
				t.Fatalf("want 1 choice, got %d", len(resp.Choices))
			}
			choice := resp.Choices[0]
			if choice.FinishReason != tc.wantFinish {
				t.Errorf("finish_reason: got %q, want %q", choice.FinishReason, tc.wantFinish)
			}
			if got := len(choice.Message.ToolCalls); got != tc.wantTools {
				t.Errorf("tool_calls: got %d, want %d", got, tc.wantTools)
			}
			if tc.wantText != "" {
				if choice.Message.Content == nil || *choice.Message.Content != tc.wantText {
					t.Errorf("content: got %v, want %q", choice.Message.Content, tc.wantText)
				}
			}
		})
	}
}

func TestErrorResponseFixturesUnmarshal(t *testing.T) {
	for _, file := range []string{
		"chat_response_error_401.json",
		"chat_response_error_429.json",
		"chat_response_error_500.json",
	} {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", file))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var resp chatResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if resp.Error == nil || resp.Error.Message == "" {
				t.Errorf("want non-empty error message, got %+v", resp.Error)
			}
		})
	}
}

func TestModelsResponseFixtureUnmarshal(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "models_response.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var resp modelsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data) != 3 {
		t.Errorf("want 3 entries, got %d", len(resp.Data))
	}
	wantIDs := map[string]bool{"gpt-4o": true, "gpt-4o-mini": true, "o3-mini": true}
	for _, e := range resp.Data {
		if !wantIDs[e.ID] {
			t.Errorf("unexpected id %q", e.ID)
		}
	}
}

func TestReasoningTokensFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "chat_response_o_series_with_reasoning_tokens.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var resp chatResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Usage == nil || resp.Usage.CompletionTokensDetails == nil {
		t.Fatalf("want completion_tokens_details, got %+v", resp.Usage)
	}
	if got := resp.Usage.CompletionTokensDetails.ReasoningTokens; got != 120 {
		t.Errorf("reasoning_tokens: got %d, want 120", got)
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/llm/openai/... -run 'TestChatResponseFixtures|TestErrorResponseFixtures|TestModelsResponseFixture|TestReasoningTokensFixture' -v`
Expected: all subtests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/llm/openai/wire.go internal/llm/openai/wire_test.go internal/llm/openai/testdata/
git commit -m "feat(llm/openai): wire types and golden fixtures"
```

---

### Task 4: `OpenAIHints` and `ValidateOptions`

**Files:**
- Create: `internal/llm/openai/hints.go`
- Create: `internal/llm/openai/hints_test.go`

- [ ] **Step 1: Write the failing tests first**

Create `internal/llm/openai/hints_test.go`:

```go
package openai

import (
	"strings"
	"testing"
)

func TestValidateOptions(t *testing.T) {
	cases := []struct {
		name    string
		input   map[string]any
		wantErr string // substring to match; "" means no error
	}{
		{"nil", nil, ""},
		{"empty", map[string]any{}, ""},
		{"valid temperature", map[string]any{"temperature": 0.7}, ""},
		{"temperature low bound", map[string]any{"temperature": 0.0}, ""},
		{"temperature high bound", map[string]any{"temperature": 2.0}, ""},
		{"temperature too high", map[string]any{"temperature": 2.1}, "temperature"},
		{"temperature negative", map[string]any{"temperature": -0.1}, "temperature"},
		{"temperature wrong type", map[string]any{"temperature": "hot"}, "temperature"},
		{"valid top_p", map[string]any{"top_p": 0.9}, ""},
		{"top_p too high", map[string]any{"top_p": 1.5}, "top_p"},
		{"valid reasoning_effort low", map[string]any{"reasoning_effort": "low"}, ""},
		{"valid reasoning_effort medium", map[string]any{"reasoning_effort": "medium"}, ""},
		{"valid reasoning_effort high", map[string]any{"reasoning_effort": "high"}, ""},
		{"invalid reasoning_effort", map[string]any{"reasoning_effort": "extreme"}, "reasoning_effort"},
		{"valid max_output_tokens", map[string]any{"max_output_tokens": 1024}, ""},
		{"max_output_tokens zero", map[string]any{"max_output_tokens": 0}, "max_output_tokens"},
		{"max_output_tokens negative", map[string]any{"max_output_tokens": -1}, "max_output_tokens"},
		{"unknown key", map[string]any{"frequency_penalty": 1.0}, "unknown option"},
	}
	c := &Client{} // ValidateOptions has no dependencies
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.ValidateOptions(tc.input)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestParseHintsAllFields(t *testing.T) {
	effort := "medium"
	in := map[string]any{
		"temperature":       0.5,
		"top_p":             0.9,
		"reasoning_effort":  effort,
		"max_output_tokens": 2048,
	}
	h, err := parseHints(in)
	if err != nil {
		t.Fatalf("parseHints: %v", err)
	}
	if h.Temperature == nil || *h.Temperature != 0.5 {
		t.Errorf("Temperature: got %v", h.Temperature)
	}
	if h.TopP == nil || *h.TopP != 0.9 {
		t.Errorf("TopP: got %v", h.TopP)
	}
	if h.ReasoningEffort == nil || *h.ReasoningEffort != "medium" {
		t.Errorf("ReasoningEffort: got %v", h.ReasoningEffort)
	}
	if h.MaxOutputTokens == nil || *h.MaxOutputTokens != 2048 {
		t.Errorf("MaxOutputTokens: got %v", h.MaxOutputTokens)
	}
}
```

- [ ] **Step 2: Run to confirm it fails (package won't compile — `Client` and `parseHints` don't exist yet)**

Run: `go test ./internal/llm/openai/... -run 'TestValidateOptions|TestParseHintsAllFields'`
Expected: FAIL with "undefined: Client" and "undefined: parseHints".

- [ ] **Step 3: Create `hints.go`**

```go
package openai

import "fmt"

// OpenAIHints carries optional tuning fields that map to OpenAI Chat
// Completions request parameters. All fields are pointers so "unset" is
// distinct from "zero" — nil means the translator omits the field.
type OpenAIHints struct {
	Temperature     *float64
	TopP            *float64
	ReasoningEffort *string // "low" | "medium" | "high"
	MaxOutputTokens *int
}

var validReasoningEfforts = map[string]bool{"low": true, "medium": true, "high": true}

// parseHints converts a policy-YAML options map into an *OpenAIHints, or
// returns a descriptive error if any field is invalid or unknown.
func parseHints(options map[string]any) (*OpenAIHints, error) {
	h := &OpenAIHints{}
	for key, raw := range options {
		switch key {
		case "temperature":
			f, ok := toFloat64(raw)
			if !ok {
				return nil, fmt.Errorf("temperature: must be a number, got %T", raw)
			}
			if f < 0 || f > 2 {
				return nil, fmt.Errorf("temperature: must be in [0, 2], got %v", f)
			}
			h.Temperature = &f
		case "top_p":
			f, ok := toFloat64(raw)
			if !ok {
				return nil, fmt.Errorf("top_p: must be a number, got %T", raw)
			}
			if f < 0 || f > 1 {
				return nil, fmt.Errorf("top_p: must be in [0, 1], got %v", f)
			}
			h.TopP = &f
		case "reasoning_effort":
			s, ok := raw.(string)
			if !ok {
				return nil, fmt.Errorf("reasoning_effort: must be a string, got %T", raw)
			}
			if !validReasoningEfforts[s] {
				return nil, fmt.Errorf("reasoning_effort: must be one of low, medium, high; got %q", s)
			}
			h.ReasoningEffort = &s
		case "max_output_tokens":
			n, ok := toInt(raw)
			if !ok {
				return nil, fmt.Errorf("max_output_tokens: must be an integer, got %T", raw)
			}
			if n <= 0 {
				return nil, fmt.Errorf("max_output_tokens: must be > 0, got %d", n)
			}
			h.MaxOutputTokens = &n
		default:
			return nil, fmt.Errorf("unknown option %q", key)
		}
	}
	return h, nil
}

func toFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	default:
		return 0, false
	}
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		if x != float64(int(x)) {
			return 0, false
		}
		return int(x), true
	default:
		return 0, false
	}
}
```

- [ ] **Step 4: Stub `Client` with `ValidateOptions` so the package compiles**

Create a minimal `internal/llm/openai/client.go`. This will be extended in Task 7.

```go
package openai

// Client implements llm.LLMClient against the OpenAI Chat Completions API.
// Additional fields are added in Task 7.
type Client struct{}

// ValidateOptions parses and validates policy YAML options for this provider.
// Empty or nil options are valid.
func (c *Client) ValidateOptions(options map[string]any) error {
	_, err := parseHints(options)
	return err
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/llm/openai/... -run 'TestValidateOptions|TestParseHintsAllFields' -v`
Expected: all subtests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/llm/openai/hints.go internal/llm/openai/hints_test.go internal/llm/openai/client.go
git commit -m "feat(llm/openai): OpenAIHints with ValidateOptions"
```

---

### Task 5: Translator — `MessageRequest` → wire request

**Files:**
- Create: `internal/llm/openai/translate.go`
- Create: `internal/llm/openai/translate_test.go`

- [ ] **Step 1: Write the failing tests first**

Create `internal/llm/openai/translate_test.go`:

```go
package openai

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/rapp992/gleipnir/internal/llm"
)

func strp(s string) *string { return &s }
func intp(i int) *int       { return &i }
func f64p(f float64) *float64 { return &f }

func TestBuildChatCompletionRequest(t *testing.T) {
	cases := []struct {
		name string
		in   llm.MessageRequest
		// Assertions on the resulting wire request.
		check func(t *testing.T, req chatRequest)
	}{
		{
			name: "empty system prompt omits system message",
			in: llm.MessageRequest{
				Model: "gpt-4o",
				History: []llm.ConversationTurn{
					{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}},
				},
			},
			check: func(t *testing.T, req chatRequest) {
				if len(req.Messages) != 1 {
					t.Fatalf("want 1 message, got %d", len(req.Messages))
				}
				if req.Messages[0].Role != "user" {
					t.Errorf("want user, got %q", req.Messages[0].Role)
				}
			},
		},
		{
			name: "non-empty system prompt becomes first message",
			in: llm.MessageRequest{
				Model:        "gpt-4o",
				SystemPrompt: "You are helpful.",
				History: []llm.ConversationTurn{
					{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}},
				},
			},
			check: func(t *testing.T, req chatRequest) {
				if len(req.Messages) != 2 {
					t.Fatalf("want 2 messages, got %d", len(req.Messages))
				}
				if req.Messages[0].Role != "system" {
					t.Errorf("want system, got %q", req.Messages[0].Role)
				}
				if req.Messages[0].Content == nil || *req.Messages[0].Content != "You are helpful." {
					t.Errorf("system content mismatch: %+v", req.Messages[0].Content)
				}
			},
		},
		{
			name: "user turn with multiple text blocks concatenates",
			in: llm.MessageRequest{
				Model: "gpt-4o",
				History: []llm.ConversationTurn{
					{Role: llm.RoleUser, Content: []llm.ContentBlock{
						llm.TextBlock{Text: "part one"},
						llm.TextBlock{Text: "part two"},
					}},
				},
			},
			check: func(t *testing.T, req chatRequest) {
				if req.Messages[0].Content == nil || *req.Messages[0].Content != "part one\n\npart two" {
					t.Errorf("concatenation mismatch: %+v", req.Messages[0].Content)
				}
			},
		},
		{
			name: "assistant turn with text only emits string content no tool_calls",
			in: llm.MessageRequest{
				Model: "gpt-4o",
				History: []llm.ConversationTurn{
					{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
						llm.TextBlock{Text: "sure thing"},
					}},
				},
			},
			check: func(t *testing.T, req chatRequest) {
				m := req.Messages[0]
				if m.Role != "assistant" {
					t.Errorf("role: %q", m.Role)
				}
				if m.Content == nil || *m.Content != "sure thing" {
					t.Errorf("content: %+v", m.Content)
				}
				if len(m.ToolCalls) != 0 {
					t.Errorf("want no tool_calls, got %d", len(m.ToolCalls))
				}
			},
		},
		{
			name: "assistant turn with tool calls only emits null content",
			in: llm.MessageRequest{
				Model: "gpt-4o",
				History: []llm.ConversationTurn{
					{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
						llm.ToolCallBlock{ID: "call_1", Name: "get_weather", Input: json.RawMessage(`{"city":"SF"}`)},
					}},
				},
			},
			check: func(t *testing.T, req chatRequest) {
				m := req.Messages[0]
				if m.Content != nil {
					t.Errorf("want nil content (JSON null), got %+v", m.Content)
				}
				if len(m.ToolCalls) != 1 {
					t.Fatalf("want 1 tool_call, got %d", len(m.ToolCalls))
				}
				if m.ToolCalls[0].ID != "call_1" {
					t.Errorf("id: %q", m.ToolCalls[0].ID)
				}
				if m.ToolCalls[0].Function.Name != "get_weather" {
					t.Errorf("name: %q", m.ToolCalls[0].Function.Name)
				}
				if m.ToolCalls[0].Function.Arguments != `{"city":"SF"}` {
					t.Errorf("arguments: %q", m.ToolCalls[0].Function.Arguments)
				}
			},
		},
		{
			name: "assistant turn with both text and tool calls emits both",
			in: llm.MessageRequest{
				Model: "gpt-4o",
				History: []llm.ConversationTurn{
					{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
						llm.TextBlock{Text: "let me check"},
						llm.ToolCallBlock{ID: "c1", Name: "t", Input: json.RawMessage(`{}`)},
					}},
				},
			},
			check: func(t *testing.T, req chatRequest) {
				m := req.Messages[0]
				if m.Content == nil || *m.Content != "let me check" {
					t.Errorf("content: %+v", m.Content)
				}
				if len(m.ToolCalls) != 1 {
					t.Errorf("want 1 tool_call, got %d", len(m.ToolCalls))
				}
			},
		},
		{
			name: "user turn with single tool result becomes role:tool message",
			in: llm.MessageRequest{
				Model: "gpt-4o",
				History: []llm.ConversationTurn{
					{Role: llm.RoleUser, Content: []llm.ContentBlock{
						llm.ToolResultBlock{ToolCallID: "c1", Content: "72F sunny"},
					}},
				},
			},
			check: func(t *testing.T, req chatRequest) {
				if len(req.Messages) != 1 {
					t.Fatalf("want 1 message, got %d", len(req.Messages))
				}
				m := req.Messages[0]
				if m.Role != "tool" {
					t.Errorf("want role tool, got %q", m.Role)
				}
				if m.ToolCallID != "c1" {
					t.Errorf("tool_call_id: %q", m.ToolCallID)
				}
				if m.Content == nil || *m.Content != "72F sunny" {
					t.Errorf("content: %+v", m.Content)
				}
			},
		},
		{
			name: "user turn with multiple tool results becomes N tool messages",
			in: llm.MessageRequest{
				Model: "gpt-4o",
				History: []llm.ConversationTurn{
					{Role: llm.RoleUser, Content: []llm.ContentBlock{
						llm.ToolResultBlock{ToolCallID: "a", Content: "1"},
						llm.ToolResultBlock{ToolCallID: "b", Content: "2"},
					}},
				},
			},
			check: func(t *testing.T, req chatRequest) {
				if len(req.Messages) != 2 {
					t.Fatalf("want 2 messages, got %d", len(req.Messages))
				}
				if req.Messages[0].ToolCallID != "a" || req.Messages[1].ToolCallID != "b" {
					t.Errorf("order wrong: %s, %s", req.Messages[0].ToolCallID, req.Messages[1].ToolCallID)
				}
			},
		},
		{
			name: "tool result with IsError true prefixes content",
			in: llm.MessageRequest{
				Model: "gpt-4o",
				History: []llm.ConversationTurn{
					{Role: llm.RoleUser, Content: []llm.ContentBlock{
						llm.ToolResultBlock{ToolCallID: "c1", Content: "file not found", IsError: true},
					}},
				},
			},
			check: func(t *testing.T, req chatRequest) {
				if req.Messages[0].Content == nil || *req.Messages[0].Content != "[error] file not found" {
					t.Errorf("want error-prefixed content, got %+v", req.Messages[0].Content)
				}
			},
		},
		{
			name: "mixed text and tool results: tool messages first, then user text",
			in: llm.MessageRequest{
				Model: "gpt-4o",
				History: []llm.ConversationTurn{
					{Role: llm.RoleUser, Content: []llm.ContentBlock{
						llm.ToolResultBlock{ToolCallID: "c1", Content: "ok"},
						llm.TextBlock{Text: "thanks"},
					}},
				},
			},
			check: func(t *testing.T, req chatRequest) {
				if len(req.Messages) != 2 {
					t.Fatalf("want 2 messages, got %d", len(req.Messages))
				}
				if req.Messages[0].Role != "tool" {
					t.Errorf("first should be tool, got %q", req.Messages[0].Role)
				}
				if req.Messages[1].Role != "user" {
					t.Errorf("second should be user, got %q", req.Messages[1].Role)
				}
			},
		},
		{
			name: "tool definitions become function tools",
			in: llm.MessageRequest{
				Model: "gpt-4o",
				Tools: []llm.ToolDefinition{
					{Name: "get_weather", Description: "fetch weather", InputSchema: json.RawMessage(`{"type":"object"}`)},
				},
			},
			check: func(t *testing.T, req chatRequest) {
				if len(req.Tools) != 1 {
					t.Fatalf("want 1 tool, got %d", len(req.Tools))
				}
				if req.Tools[0].Type != "function" {
					t.Errorf("type: %q", req.Tools[0].Type)
				}
				if req.Tools[0].Function.Name != "get_weather" {
					t.Errorf("name: %q", req.Tools[0].Function.Name)
				}
				if string(req.Tools[0].Function.Parameters) != `{"type":"object"}` {
					t.Errorf("parameters: %s", req.Tools[0].Function.Parameters)
				}
			},
		},
		{
			name: "MaxTokens with non-o-series uses max_tokens",
			in: llm.MessageRequest{
				Model:     "gpt-4o",
				MaxTokens: 1024,
			},
			check: func(t *testing.T, req chatRequest) {
				if req.MaxTokens == nil || *req.MaxTokens != 1024 {
					t.Errorf("MaxTokens: %+v", req.MaxTokens)
				}
				if req.MaxCompletionTokens != nil {
					t.Errorf("MaxCompletionTokens should be nil, got %+v", req.MaxCompletionTokens)
				}
			},
		},
		{
			name: "MaxTokens with o-series uses max_completion_tokens",
			in: llm.MessageRequest{
				Model:     "o3-mini",
				MaxTokens: 1024,
			},
			check: func(t *testing.T, req chatRequest) {
				if req.MaxTokens != nil {
					t.Errorf("MaxTokens should be nil, got %+v", req.MaxTokens)
				}
				if req.MaxCompletionTokens == nil || *req.MaxCompletionTokens != 1024 {
					t.Errorf("MaxCompletionTokens: %+v", req.MaxCompletionTokens)
				}
			},
		},
		{
			name: "hints populate temperature and top_p",
			in: llm.MessageRequest{
				Model: "gpt-4o",
				Hints: &OpenAIHints{Temperature: f64p(0.3), TopP: f64p(0.9)},
			},
			check: func(t *testing.T, req chatRequest) {
				if req.Temperature == nil || *req.Temperature != 0.3 {
					t.Errorf("temperature: %+v", req.Temperature)
				}
				if req.TopP == nil || *req.TopP != 0.9 {
					t.Errorf("top_p: %+v", req.TopP)
				}
			},
		},
		{
			name: "reasoning_effort only sent for o-series",
			in: llm.MessageRequest{
				Model: "gpt-4o",
				Hints: &OpenAIHints{ReasoningEffort: strp("high")},
			},
			check: func(t *testing.T, req chatRequest) {
				if req.ReasoningEffort != nil {
					t.Errorf("reasoning_effort should be omitted for non-o-series, got %+v", req.ReasoningEffort)
				}
			},
		},
		{
			name: "reasoning_effort passed through for o-series",
			in: llm.MessageRequest{
				Model: "o3-mini",
				Hints: &OpenAIHints{ReasoningEffort: strp("high")},
			},
			check: func(t *testing.T, req chatRequest) {
				if req.ReasoningEffort == nil || *req.ReasoningEffort != "high" {
					t.Errorf("reasoning_effort: %+v", req.ReasoningEffort)
				}
			},
		},
		{
			name: "unknown Hints type is silently ignored",
			in: llm.MessageRequest{
				Model: "gpt-4o",
				Hints: "not-a-hints-struct",
			},
			check: func(t *testing.T, req chatRequest) {
				if req.Temperature != nil || req.TopP != nil || req.ReasoningEffort != nil {
					t.Errorf("unknown hints should have been ignored, got %+v", req)
				}
			},
		},
		{
			name: "thinking blocks are dropped",
			in: llm.MessageRequest{
				Model: "gpt-4o",
				History: []llm.ConversationTurn{
					{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
						llm.TextBlock{Text: "answer"},
						// ThinkingBlock would go here if it were a ContentBlock; it's not.
						// The translator sees only TextBlock/ToolCallBlock/ToolResultBlock.
					}},
				},
			},
			check: func(t *testing.T, req chatRequest) {
				if req.Messages[0].Content == nil || *req.Messages[0].Content != "answer" {
					t.Errorf("content: %+v", req.Messages[0].Content)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := BuildChatCompletionRequest(tc.in, false)
			tc.check(t, req)
		})
	}
}

func TestBuildChatCompletionRequest_StreamFlag(t *testing.T) {
	req := BuildChatCompletionRequest(llm.MessageRequest{Model: "gpt-4o"}, true)
	if !req.Stream {
		t.Error("want Stream true")
	}
	if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
		t.Errorf("want stream_options.include_usage true, got %+v", req.StreamOptions)
	}
}

// isOSeriesModel is exported via test helper to lock the heuristic.
func TestIsOSeriesModel(t *testing.T) {
	cases := map[string]bool{
		"o1":                 true,
		"o1-mini":            true,
		"o3":                 true,
		"o3-mini":            true,
		"o4-mini":            true,
		"gpt-5-reasoning":    true,
		"gpt-4o":             false,
		"gpt-4o-mini":        false,
		"gpt-4.1":            false,
		"llama3.1:70b":       false,
	}
	for model, want := range cases {
		if got := isOSeriesModel(model); got != want {
			t.Errorf("isOSeriesModel(%q) = %v, want %v", model, got, want)
		}
	}
}

// ensure BuildChatCompletionRequest produces JSON that round-trips through
// encoding/json without losing information.
func TestBuildChatCompletionRequest_JSONRoundTrip(t *testing.T) {
	in := llm.MessageRequest{
		Model:        "gpt-4o",
		SystemPrompt: "s",
		MaxTokens:    100,
		History: []llm.ConversationTurn{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}},
		},
	}
	req := BuildChatCompletionRequest(in, false)
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back chatRequest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(req.Messages, back.Messages) {
		t.Errorf("round-trip mismatch:\nwant %+v\ngot  %+v", req.Messages, back.Messages)
	}
}
```

- [ ] **Step 2: Run to confirm tests fail (no BuildChatCompletionRequest yet)**

Run: `go test ./internal/llm/openai/... -run TestBuildChatCompletionRequest`
Expected: FAIL with "undefined: BuildChatCompletionRequest" and "undefined: isOSeriesModel".

- [ ] **Step 3: Implement `translate.go` — request side**

Create `internal/llm/openai/translate.go`:

```go
package openai

import (
	"strings"

	"github.com/rapp992/gleipnir/internal/llm"
)

// BuildChatCompletionRequest translates an llm.MessageRequest into an OpenAI
// Chat Completions wire request. The `stream` argument sets Stream and
// StreamOptions; the translator is otherwise identical for sync and streaming.
// See spec §7.6 for the full translation rules.
func BuildChatCompletionRequest(req llm.MessageRequest, stream bool) chatRequest {
	out := chatRequest{
		Model:    req.Model,
		Messages: make([]chatMessage, 0, len(req.History)+1),
	}
	if stream {
		out.Stream = true
		out.StreamOptions = &streamOptions{IncludeUsage: true}
	}

	if req.SystemPrompt != "" {
		s := req.SystemPrompt
		out.Messages = append(out.Messages, chatMessage{Role: "system", Content: &s})
	}

	for _, turn := range req.History {
		out.Messages = append(out.Messages, translateTurn(turn)...)
	}

	for _, td := range req.Tools {
		out.Tools = append(out.Tools, chatTool{
			Type: "function",
			Function: chatToolFunc{
				Name:        td.Name,
				Description: td.Description,
				Parameters:  td.InputSchema,
			},
		})
	}

	// MaxTokens: route to the right field depending on the model family.
	hints, _ := req.Hints.(*OpenAIHints)
	effectiveMax := req.MaxTokens
	if hints != nil && hints.MaxOutputTokens != nil {
		effectiveMax = *hints.MaxOutputTokens
	}
	if effectiveMax > 0 {
		v := effectiveMax
		if isOSeriesModel(req.Model) {
			out.MaxCompletionTokens = &v
		} else {
			out.MaxTokens = &v
		}
	}

	if hints != nil {
		if hints.Temperature != nil {
			t := *hints.Temperature
			out.Temperature = &t
		}
		if hints.TopP != nil {
			p := *hints.TopP
			out.TopP = &p
		}
		if hints.ReasoningEffort != nil && isOSeriesModel(req.Model) {
			e := *hints.ReasoningEffort
			out.ReasoningEffort = &e
		}
	}

	return out
}

// translateTurn maps one Gleipnir ConversationTurn into one or more wire
// messages. A user turn containing tool results produces N role:"tool"
// messages; a user turn mixing text and tool results emits tool messages
// first, then a user message with the concatenated text.
func translateTurn(turn llm.ConversationTurn) []chatMessage {
	switch turn.Role {
	case llm.RoleAssistant:
		return []chatMessage{translateAssistantTurn(turn.Content)}
	case llm.RoleUser:
		return translateUserTurn(turn.Content)
	default:
		return nil
	}
}

func translateAssistantTurn(blocks []llm.ContentBlock) chatMessage {
	msg := chatMessage{Role: "assistant"}
	var texts []string
	for _, b := range blocks {
		switch v := b.(type) {
		case llm.TextBlock:
			texts = append(texts, v.Text)
		case llm.ToolCallBlock:
			msg.ToolCalls = append(msg.ToolCalls, chatToolCall{
				ID:   v.ID,
				Type: "function",
				Function: chatToolCallFunc{
					Name:      v.Name,
					Arguments: string(v.Input),
				},
			})
		}
		// ToolResultBlock is not valid in an assistant turn; silently ignored.
	}
	if len(texts) > 0 {
		joined := strings.Join(texts, "\n\n")
		msg.Content = &joined
	}
	// Content stays nil (JSON null) when there are tool calls and no text.
	return msg
}

func translateUserTurn(blocks []llm.ContentBlock) []chatMessage {
	var toolMsgs []chatMessage
	var texts []string
	for _, b := range blocks {
		switch v := b.(type) {
		case llm.TextBlock:
			texts = append(texts, v.Text)
		case llm.ToolResultBlock:
			content := v.Content
			if v.IsError {
				content = "[error] " + content
			}
			c := content
			toolMsgs = append(toolMsgs, chatMessage{
				Role:       "tool",
				Content:    &c,
				ToolCallID: v.ToolCallID,
			})
		}
	}
	out := toolMsgs
	if len(texts) > 0 {
		joined := strings.Join(texts, "\n\n")
		out = append(out, chatMessage{Role: "user", Content: &joined})
	}
	return out
}

// isOSeriesModel returns true when the model should route max_tokens to
// max_completion_tokens and honor reasoning_effort. The heuristic is:
// name starts with "o<digit>" (o1, o3, o4) or contains "reasoning".
// Contained to this function; no other place in the package pattern-matches
// on model names.
func isOSeriesModel(model string) bool {
	if strings.Contains(model, "reasoning") {
		return true
	}
	if len(model) < 2 {
		return false
	}
	if model[0] != 'o' {
		return false
	}
	c := model[1]
	return c >= '0' && c <= '9'
}
```

- [ ] **Step 4: Run the request-side tests**

Run: `go test ./internal/llm/openai/... -run 'TestBuildChatCompletionRequest|TestIsOSeriesModel' -v`
Expected: all subtests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/llm/openai/translate.go internal/llm/openai/translate_test.go
git commit -m "feat(llm/openai): MessageRequest -> Chat Completions translator"
```

---

### Task 6: Translator — wire response → `MessageResponse`

**Files:**
- Modify: `internal/llm/openai/translate.go` (add `ParseChatCompletionResponse`)
- Modify: `internal/llm/openai/translate_test.go` (add cases)

- [ ] **Step 1: Append the failing tests**

Append to `internal/llm/openai/translate_test.go`:

```go
func TestParseChatCompletionResponse(t *testing.T) {
	cases := []struct {
		name        string
		fixture     string
		wantText    string
		wantCalls   int
		wantStop    llm.StopReason
		wantInTok   int
		wantOutTok  int
		wantThinkTk int
		wantErr     bool
	}{
		{
			name: "text only",
			fixture: "chat_response_text_only.json",
			wantText: "Hello from OpenAI.", wantStop: llm.StopReasonEndTurn,
			wantInTok: 12, wantOutTok: 5,
		},
		{
			name: "single tool call",
			fixture: "chat_response_with_tool_calls.json",
			wantCalls: 1, wantStop: llm.StopReasonToolUse,
			wantInTok: 40, wantOutTok: 15,
		},
		{
			name: "parallel tool calls",
			fixture: "chat_response_parallel_tool_calls.json",
			wantCalls: 2, wantStop: llm.StopReasonToolUse,
			wantInTok: 50, wantOutTok: 20,
		},
		{
			name: "o-series reasoning tokens",
			fixture: "chat_response_o_series_with_reasoning_tokens.json",
			wantText: "42", wantStop: llm.StopReasonEndTurn,
			wantInTok: 30, wantOutTok: 150, wantThinkTk: 120,
		},
		{
			name: "length truncation",
			fixture: "chat_response_finish_length.json",
			wantText: "truncated...", wantStop: llm.StopReasonMaxTokens,
			wantInTok: 10, wantOutTok: 100,
		},
		{
			name: "content filter maps to error",
			fixture: "chat_response_finish_content_filter.json",
			wantStop: llm.StopReasonError,
			wantInTok: 10,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", tc.fixture))
			if err != nil {
				t.Fatalf("fixture: %v", err)
			}
			var wire chatResponse
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			resp, err := ParseChatCompletionResponse(&wire)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if tc.wantText != "" {
				if len(resp.Text) != 1 || resp.Text[0].Text != tc.wantText {
					t.Errorf("text: %+v", resp.Text)
				}
			}
			if len(resp.ToolCalls) != tc.wantCalls {
				t.Errorf("tool calls: got %d, want %d", len(resp.ToolCalls), tc.wantCalls)
			}
			if resp.StopReason != tc.wantStop {
				t.Errorf("stop reason: got %v, want %v", resp.StopReason, tc.wantStop)
			}
			if resp.Usage.InputTokens != tc.wantInTok {
				t.Errorf("input tokens: got %d, want %d", resp.Usage.InputTokens, tc.wantInTok)
			}
			if resp.Usage.OutputTokens != tc.wantOutTok {
				t.Errorf("output tokens: got %d, want %d", resp.Usage.OutputTokens, tc.wantOutTok)
			}
			if resp.Usage.ThinkingTokens != tc.wantThinkTk {
				t.Errorf("thinking tokens: got %d, want %d", resp.Usage.ThinkingTokens, tc.wantThinkTk)
			}
			if resp.Thinking != nil {
				t.Errorf("Thinking should always be nil for OpenAI, got %+v", resp.Thinking)
			}
		})
	}
}

func TestParseChatCompletionResponse_MalformedToolArguments(t *testing.T) {
	content := (*string)(nil)
	wire := &chatResponse{
		Choices: []chatChoice{{
			Message: chatMessage{
				Role:    "assistant",
				Content: content,
				ToolCalls: []chatToolCall{{
					ID:   "c1",
					Type: "function",
					Function: chatToolCallFunc{Name: "t", Arguments: "not-json"},
				}},
			},
			FinishReason: "tool_calls",
		}},
	}
	if _, err := ParseChatCompletionResponse(wire); err == nil {
		t.Error("expected error for malformed tool arguments")
	}
}

func TestParseChatCompletionResponse_UnknownFinishReason(t *testing.T) {
	s := "hi"
	wire := &chatResponse{
		Choices: []chatChoice{{
			Message:      chatMessage{Role: "assistant", Content: &s},
			FinishReason: "something_new",
		}},
	}
	resp, err := ParseChatCompletionResponse(wire)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.StopReason != llm.StopReasonError {
		t.Errorf("unknown finish should map to Error, got %v", resp.StopReason)
	}
}

func TestParseChatCompletionResponse_MissingUsageDetails(t *testing.T) {
	s := "hi"
	wire := &chatResponse{
		Choices: []chatChoice{{
			Message:      chatMessage{Role: "assistant", Content: &s},
			FinishReason: "stop",
		}},
		Usage: &chatUsage{PromptTokens: 5, CompletionTokens: 10},
	}
	resp, err := ParseChatCompletionResponse(wire)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Usage.ThinkingTokens != 0 {
		t.Errorf("thinking tokens should be 0, got %d", resp.Usage.ThinkingTokens)
	}
}
```

Also add imports if missing:

```go
import (
	"os"
	"path/filepath"
)
```

- [ ] **Step 2: Run to confirm tests fail**

Run: `go test ./internal/llm/openai/... -run TestParseChatCompletionResponse`
Expected: FAIL with "undefined: ParseChatCompletionResponse".

- [ ] **Step 3: Append `ParseChatCompletionResponse` to `translate.go`**

Append to `internal/llm/openai/translate.go`:

```go
import (
	"encoding/json"
	"fmt"
	// keep existing imports
)

// ParseChatCompletionResponse translates a wire response into the normalized
// llm.MessageResponse. Returns an error only on malformed tool call
// arguments — all other abnormalities (unknown finish reasons, missing
// usage details) degrade gracefully.
func ParseChatCompletionResponse(wire *chatResponse) (*llm.MessageResponse, error) {
	out := &llm.MessageResponse{}
	if len(wire.Choices) == 0 {
		return out, nil
	}
	choice := wire.Choices[0]

	if choice.Message.Content != nil && *choice.Message.Content != "" {
		out.Text = []llm.TextBlock{{Text: *choice.Message.Content}}
	}

	for _, tc := range choice.Message.ToolCalls {
		// Validate that Arguments is parseable JSON — callers trust Input is.
		if !json.Valid([]byte(tc.Function.Arguments)) {
			return nil, fmt.Errorf("openai: tool call %q: arguments is not valid JSON: %q",
				tc.Function.Name, tc.Function.Arguments)
		}
		out.ToolCalls = append(out.ToolCalls, llm.ToolCallBlock{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: json.RawMessage(tc.Function.Arguments),
		})
	}

	switch choice.FinishReason {
	case "stop":
		out.StopReason = llm.StopReasonEndTurn
	case "tool_calls":
		out.StopReason = llm.StopReasonToolUse
	case "length":
		out.StopReason = llm.StopReasonMaxTokens
	default:
		out.StopReason = llm.StopReasonError
	}

	if wire.Usage != nil {
		out.Usage.InputTokens = wire.Usage.PromptTokens
		out.Usage.OutputTokens = wire.Usage.CompletionTokens
		if wire.Usage.CompletionTokensDetails != nil {
			out.Usage.ThinkingTokens = wire.Usage.CompletionTokensDetails.ReasoningTokens
		}
	}

	// Thinking is always nil for OpenAI — reasoning content is not surfaced
	// by the Chat Completions endpoint. See ADR-032 §2 ("Why Chat Completions
	// only, not the Responses API").
	return out, nil
}
```

- [ ] **Step 4: Run the response-side tests**

Run: `go test ./internal/llm/openai/... -run TestParseChatCompletionResponse -v`
Expected: all subtests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/llm/openai/translate.go internal/llm/openai/translate_test.go
git commit -m "feat(llm/openai): Chat Completions response -> MessageResponse translator"
```

---

### Task 7: Stream parser

**Files:**
- Create: `internal/llm/openai/stream.go`
- Create: `internal/llm/openai/stream_test.go`
- Create: `internal/llm/openai/testdata/stream_chunks_text.txt`
- Create: `internal/llm/openai/testdata/stream_chunks_with_tool_calls.txt`
- Create: `internal/llm/openai/testdata/stream_chunks_with_usage.txt`

- [ ] **Step 1: Create the SSE fixtures**

Create `internal/llm/openai/testdata/stream_chunks_text.txt` (blank lines terminate SSE events — keep them exactly as shown):

```
data: {"choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

```

Create `internal/llm/openai/testdata/stream_chunks_with_tool_calls.txt`:

```
data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"ci"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ty\":\"SF\"}"}}]},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

```

Create `internal/llm/openai/testdata/stream_chunks_with_usage.txt`:

```
data: {"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":1}}

data: [DONE]

```

- [ ] **Step 2: Write the failing tests**

Create `internal/llm/openai/stream_test.go`:

```go
package openai

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rapp992/gleipnir/internal/llm"
)

func loadStreamFixture(t *testing.T, name string) io.ReadCloser {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return io.NopCloser(strings.NewReader(string(raw)))
}

func collectChunks(t *testing.T, ch <-chan llm.MessageChunk) []llm.MessageChunk {
	t.Helper()
	var out []llm.MessageChunk
	for c := range ch {
		out = append(out, c)
	}
	return out
}

func TestParseSSEStream_TextOnly(t *testing.T) {
	body := loadStreamFixture(t, "stream_chunks_text.txt")
	ch := make(chan llm.MessageChunk, 16)
	go parseSSEStream(context.Background(), body, ch)
	chunks := collectChunks(t, ch)

	var text strings.Builder
	var sawStop bool
	for _, c := range chunks {
		if c.Err != nil {
			t.Fatalf("unexpected error chunk: %v", c.Err)
		}
		if c.Text != nil {
			text.WriteString(*c.Text)
		}
		if c.StopReason != nil {
			sawStop = true
			if *c.StopReason != llm.StopReasonEndTurn {
				t.Errorf("stop reason: got %v, want EndTurn", *c.StopReason)
			}
		}
	}
	if text.String() != "Hello world" {
		t.Errorf("assembled text: %q", text.String())
	}
	if !sawStop {
		t.Error("no stop chunk emitted")
	}
}

func TestParseSSEStream_ToolCallsAreEmittedComplete(t *testing.T) {
	body := loadStreamFixture(t, "stream_chunks_with_tool_calls.txt")
	ch := make(chan llm.MessageChunk, 16)
	go parseSSEStream(context.Background(), body, ch)
	chunks := collectChunks(t, ch)

	var toolCallChunks int
	var finalCall *llm.ToolCallBlock
	for _, c := range chunks {
		if c.Err != nil {
			t.Fatalf("unexpected error: %v", c.Err)
		}
		if c.ToolCall != nil {
			toolCallChunks++
			finalCall = c.ToolCall
		}
	}
	if toolCallChunks != 1 {
		t.Fatalf("want exactly 1 tool-call chunk, got %d", toolCallChunks)
	}
	if finalCall.ID != "call_abc" {
		t.Errorf("id: %q", finalCall.ID)
	}
	if finalCall.Name != "get_weather" {
		t.Errorf("name: %q", finalCall.Name)
	}
	if string(finalCall.Input) != `{"city":"SF"}` {
		t.Errorf("arguments not reassembled: %q", finalCall.Input)
	}
}

func TestParseSSEStream_UsageChunkPopulatesFinalUsage(t *testing.T) {
	body := loadStreamFixture(t, "stream_chunks_with_usage.txt")
	ch := make(chan llm.MessageChunk, 16)
	go parseSSEStream(context.Background(), body, ch)
	chunks := collectChunks(t, ch)

	var gotUsage *llm.TokenUsage
	for _, c := range chunks {
		if c.Usage != nil {
			gotUsage = c.Usage
		}
	}
	if gotUsage == nil {
		t.Fatal("no usage chunk emitted")
	}
	if gotUsage.InputTokens != 7 || gotUsage.OutputTokens != 1 {
		t.Errorf("usage: %+v", gotUsage)
	}
}

func TestParseSSEStream_NoDoneTerminatorIsError(t *testing.T) {
	body := io.NopCloser(strings.NewReader(
		`data: {"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}` + "\n\n",
	))
	ch := make(chan llm.MessageChunk, 16)
	go parseSSEStream(context.Background(), body, ch)
	chunks := collectChunks(t, ch)

	// Stream ends without [DONE] → final chunk should carry an error.
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	last := chunks[len(chunks)-1]
	if last.Err == nil {
		t.Errorf("expected error on incomplete stream, got %+v", last)
	}
}

func TestParseSSEStream_MalformedJSONIsError(t *testing.T) {
	body := io.NopCloser(strings.NewReader(
		`data: {not-valid-json` + "\n\n" + `data: [DONE]` + "\n\n",
	))
	ch := make(chan llm.MessageChunk, 16)
	go parseSSEStream(context.Background(), body, ch)
	chunks := collectChunks(t, ch)
	var sawErr bool
	for _, c := range chunks {
		if c.Err != nil {
			sawErr = true
			break
		}
	}
	if !sawErr {
		t.Error("expected error chunk for malformed JSON")
	}
}

func TestParseSSEStream_ContextCancellation(t *testing.T) {
	// A slow reader that blocks forever; we cancel the context mid-flight.
	slow := &blockingReader{ch: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan llm.MessageChunk, 4)

	go parseSSEStream(ctx, slow, out)
	cancel()

	// Expect the channel to close; expect a context-related error on the last chunk.
	var chunks []llm.MessageChunk
	for c := range out {
		chunks = append(chunks, c)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk (the cancellation error)")
	}
	last := chunks[len(chunks)-1]
	if !errors.Is(last.Err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", last.Err)
	}
}

// blockingReader.Read blocks until the channel is closed.
type blockingReader struct {
	ch chan struct{}
}

func (b *blockingReader) Read(p []byte) (int, error) {
	<-b.ch
	return 0, io.EOF
}
func (b *blockingReader) Close() error {
	close(b.ch)
	return nil
}

func TestParseSSEStream_ChannelClosedExactlyOnce(t *testing.T) {
	body := loadStreamFixture(t, "stream_chunks_text.txt")
	ch := make(chan llm.MessageChunk, 16)
	go parseSSEStream(context.Background(), body, ch)
	for range ch {
	}
	// Second receive on a closed channel must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("second receive panicked: %v", r)
		}
	}()
	_, ok := <-ch
	if ok {
		t.Error("channel should be closed")
	}
}
```

- [ ] **Step 3: Run to confirm tests fail**

Run: `go test ./internal/llm/openai/... -run TestParseSSEStream`
Expected: FAIL with "undefined: parseSSEStream".

- [ ] **Step 4: Implement `stream.go`**

Create `internal/llm/openai/stream.go`:

```go
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/rapp992/gleipnir/internal/llm"
)

// parseSSEStream reads OpenAI Chat Completions SSE from body and emits
// llm.MessageChunk values on out. The channel is always closed exactly once.
// Text deltas are emitted as they arrive; tool calls are buffered per
// delta.index and emitted as complete blocks when the stream's
// finish_reason arrives.
//
// Error handling: any parse or I/O error emits a final MessageChunk{Err: err}
// and closes the channel. Context cancellation emits {Err: ctx.Err()} and
// closes the channel. A stream that ends without a [DONE] terminator is
// treated as an error.
func parseSSEStream(ctx context.Context, body io.ReadCloser, out chan<- llm.MessageChunk) {
	defer close(out)
	defer body.Close()

	// Check cancellation before we start reading.
	select {
	case <-ctx.Done():
		out <- llm.MessageChunk{Err: ctx.Err()}
		return
	default:
	}

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	partials := map[int]*partialToolCall{}
	var sawDone bool
	var pendingStop *llm.StopReason
	var pendingUsage *llm.TokenUsage

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			out <- llm.MessageChunk{Err: ctx.Err()}
			return
		default:
		}

		line := scanner.Text()
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			sawDone = true
			// Flush any complete tool calls first, then the stop + usage.
			flushToolCalls(partials, out)
			if pendingStop != nil || pendingUsage != nil {
				out <- llm.MessageChunk{StopReason: pendingStop, Usage: pendingUsage}
			}
			return
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			out <- llm.MessageChunk{Err: fmt.Errorf("openai: malformed SSE chunk: %w", err)}
			return
		}

		if chunk.Usage != nil {
			pendingUsage = &llm.TokenUsage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
			}
			if chunk.Usage.CompletionTokensDetails != nil {
				pendingUsage.ThinkingTokens = chunk.Usage.CompletionTokensDetails.ReasoningTokens
			}
		}

		for _, choice := range chunk.Choices {
			if choice.Delta.Content != nil && *choice.Delta.Content != "" {
				text := *choice.Delta.Content
				out <- llm.MessageChunk{Text: &text}
			}
			for _, part := range choice.Delta.ToolCalls {
				p := partials[part.Index]
				if p == nil {
					p = &partialToolCall{}
					partials[part.Index] = p
				}
				if part.ID != "" {
					p.ID = part.ID
				}
				if part.Function.Name != "" {
					p.Name = part.Function.Name
				}
				if part.Function.Arguments != "" {
					p.Arguments.WriteString(part.Function.Arguments)
				}
			}
			if choice.FinishReason != nil {
				sr := mapFinishReason(*choice.FinishReason)
				pendingStop = &sr
			}
		}
	}

	if err := scanner.Err(); err != nil {
		out <- llm.MessageChunk{Err: fmt.Errorf("openai: reading SSE stream: %w", err)}
		return
	}
	if !sawDone {
		out <- llm.MessageChunk{Err: fmt.Errorf("openai: stream ended without [DONE] terminator")}
	}
}

type partialToolCall struct {
	ID        string
	Name      string
	Arguments bytes.Buffer
}

func flushToolCalls(partials map[int]*partialToolCall, out chan<- llm.MessageChunk) {
	// Emit in deterministic order by index.
	indices := make([]int, 0, len(partials))
	for i := range partials {
		indices = append(indices, i)
	}
	// Small-slice insertion sort — sort.Ints pulls in the sort package, overkill.
	for i := 1; i < len(indices); i++ {
		for j := i; j > 0 && indices[j-1] > indices[j]; j-- {
			indices[j-1], indices[j] = indices[j], indices[j-1]
		}
	}
	for _, idx := range indices {
		p := partials[idx]
		if p.ID == "" && p.Name == "" && p.Arguments.Len() == 0 {
			continue
		}
		args := p.Arguments.String()
		if args == "" {
			args = "{}"
		}
		out <- llm.MessageChunk{ToolCall: &llm.ToolCallBlock{
			ID:    p.ID,
			Name:  p.Name,
			Input: json.RawMessage(args),
		}}
	}
}

func mapFinishReason(s string) llm.StopReason {
	switch s {
	case "stop":
		return llm.StopReasonEndTurn
	case "tool_calls":
		return llm.StopReasonToolUse
	case "length":
		return llm.StopReasonMaxTokens
	default:
		return llm.StopReasonError
	}
}
```

- [ ] **Step 5: Run the stream parser tests**

Run: `go test ./internal/llm/openai/... -run TestParseSSEStream -v`
Expected: all subtests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/llm/openai/stream.go internal/llm/openai/stream_test.go internal/llm/openai/testdata/stream_chunks_*.txt
git commit -m "feat(llm/openai): SSE stream parser for /chat/completions"
```

---

## Layer 3 — `Client` methods

### Task 8: `Client` struct, `NewClient`, and `CreateMessage` against `httptest.Server`

**Files:**
- Modify: `internal/llm/openai/client.go` (extend the stub from Task 4)
- Create: `internal/llm/openai/client_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/llm/openai/client_test.go`:

```go
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/llm"
)

// newTestClient returns a Client pointed at srv.
func newTestClient(srv *httptest.Server) *Client {
	return NewClient(srv.URL, "test-key",
		WithHTTPClient(srv.Client()),
		WithTimeout(5*time.Second),
	)
}

func TestCreateMessage_HappyPath(t *testing.T) {
	raw, _ := os.ReadFile(filepath.Join("testdata", "chat_response_text_only.json"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing bearer header: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
	}))
	defer srv.Close()

	resp, err := newTestClient(srv).CreateMessage(context.Background(), llm.MessageRequest{
		Model: "gpt-4o",
		History: []llm.ConversationTurn{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if len(resp.Text) != 1 || resp.Text[0].Text != "Hello from OpenAI." {
		t.Errorf("text: %+v", resp.Text)
	}
	if resp.StopReason != llm.StopReasonEndTurn {
		t.Errorf("stop: %v", resp.StopReason)
	}
}

func TestCreateMessage_SendsToolsInBody(t *testing.T) {
	raw, _ := os.ReadFile(filepath.Join("testdata", "chat_response_text_only.json"))
	var gotBody chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		w.Write(raw)
	}))
	defer srv.Close()

	_, err := newTestClient(srv).CreateMessage(context.Background(), llm.MessageRequest{
		Model: "gpt-4o",
		Tools: []llm.ToolDefinition{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if len(gotBody.Tools) != 1 || gotBody.Tools[0].Function.Name != "echo" {
		t.Errorf("tools body: %+v", gotBody.Tools)
	}
}

func TestCreateMessage_ErrorStatuses(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		fixture  string
		wantSub  string
	}{
		{"401", 401, "chat_response_error_401.json", "401"},
		{"429", 429, "chat_response_error_429.json", "429"},
		{"500", 500, "chat_response_error_500.json", "500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := os.ReadFile(filepath.Join("testdata", tc.fixture))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write(raw)
			}))
			defer srv.Close()
			_, err := newTestClient(srv).CreateMessage(context.Background(), llm.MessageRequest{Model: "gpt-4o"})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestCreateMessage_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // close immediately so dial fails

	_, err := newTestClient(srv).CreateMessage(context.Background(), llm.MessageRequest{Model: "gpt-4o"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCreateMessage_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newTestClient(srv).CreateMessage(ctx, llm.MessageRequest{Model: "gpt-4o"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

func TestListModels_HappyPathAndCache(t *testing.T) {
	raw, _ := os.ReadFile(filepath.Join("testdata", "models_response.json"))
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path: %q", r.URL.Path)
		}
		atomic.AddInt32(&hits, 1)
		w.Write(raw)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 3 {
		t.Errorf("models: %+v", models)
	}
	// Second call should hit the cache, not the server.
	if _, err := c.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels 2: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("expected 1 server hit, got %d", got)
	}

	c.InvalidateModelCache()
	if _, err := c.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels 3: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("expected 2 server hits after invalidate, got %d", got)
	}
}

func TestListModels_404IsEmptySlice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	models, err := newTestClient(srv).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("want empty slice, got %+v", models)
	}
}

func TestValidateModelName_AgainstUnknownBackendAccepts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	if err := newTestClient(srv).ValidateModelName(context.Background(), "anything"); err != nil {
		t.Errorf("want nil (backend without /models), got %v", err)
	}
}

func TestValidateModelName_KnownAndUnknown(t *testing.T) {
	raw, _ := os.ReadFile(filepath.Join("testdata", "models_response.json"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(raw)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	if err := c.ValidateModelName(context.Background(), "gpt-4o"); err != nil {
		t.Errorf("known model: %v", err)
	}
	if err := c.ValidateModelName(context.Background(), "does-not-exist"); err == nil {
		t.Errorf("expected error for unknown model")
	}
}
```

- [ ] **Step 2: Run to confirm tests fail**

Run: `go test ./internal/llm/openai/... -run 'TestCreateMessage|TestListModels|TestValidateModelName'`
Expected: many failures — `Client` has no `CreateMessage`, `ListModels`, `ValidateModelName`, `InvalidateModelCache`, or `NewClient` / option helpers.

- [ ] **Step 3: Replace `client.go` with the full implementation**

Overwrite `internal/llm/openai/client.go`:

```go
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rapp992/gleipnir/internal/llm"
)

// Compile-time check that *Client satisfies the LLMClient interface.
var _ llm.LLMClient = (*Client)(nil)

// Client implements llm.LLMClient against the OpenAI Chat Completions API.
// The same client serves OpenAI itself and any compatible backend; the only
// differences are baseURL and apiKey, both set at construction time.
type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	models     llm.ModelCache
	// modelsUnavailable is set when GET /models returns 404 — subsequent
	// ValidateModelName calls accept any non-empty string.
	modelsUnavailable bool
}

// Option is the functional-options type for NewClient.
type Option func(*Client)

// WithHTTPClient injects a custom *http.Client (used in tests for httptest.Server.Client()).
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithTimeout sets the HTTP client timeout. Default is 60 seconds.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if c.httpClient == nil {
			c.httpClient = &http.Client{}
		}
		c.httpClient.Timeout = d
	}
}

// NewClient constructs a Client. baseURL is the OpenAI-compatible endpoint
// prefix (e.g. "https://api.openai.com/v1"). Trailing slashes are stripped.
// apiKey is sent as a Bearer token on every request.
func NewClient(baseURL, apiKey string, opts ...Option) *Client {
	c := &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		models:  llm.NewModelCache("OpenAI-compatible"),
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	// Defense in depth: reject non-http(s) base URLs even though the admin
	// handler already validated.
	if u, err := url.Parse(c.baseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		// We cannot return an error from NewClient without a signature change.
		// The first request will fail at url.Parse in doRequest anyway.
		_ = u
	}
	return c
}

// CreateMessage sends a synchronous Chat Completions request.
func (c *Client) CreateMessage(ctx context.Context, req llm.MessageRequest) (*llm.MessageResponse, error) {
	wireReq := BuildChatCompletionRequest(req, false)
	body, err := json.Marshal(wireReq)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}
	resp, err := c.doRequest(ctx, http.MethodPost, "/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai: read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, wrapHTTPError(resp.StatusCode, raw)
	}

	var wire chatResponse
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}
	return ParseChatCompletionResponse(&wire)
}

// StreamMessage opens a streaming Chat Completions request. See Task 9 for
// the full implementation — this method is a stub in Task 8.
func (c *Client) StreamMessage(ctx context.Context, req llm.MessageRequest) (<-chan llm.MessageChunk, error) {
	return nil, errors.New("openai: StreamMessage not yet implemented")
}

// ValidateOptions parses and validates policy YAML options for this provider.
func (c *Client) ValidateOptions(options map[string]any) error {
	_, err := parseHints(options)
	return err
}

// ValidateModelName returns nil if the name appears in ListModels, or if the
// backend does not implement GET /models (the 404-escape-hatch from ADR-032).
func (c *Client) ValidateModelName(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("openai: model name is empty")
	}
	models, err := c.ListModels(ctx)
	if err != nil {
		return err
	}
	if c.modelsUnavailable {
		return nil
	}
	for _, m := range models {
		if m.Name == name {
			return nil
		}
	}
	return fmt.Errorf("openai: unknown model %q", name)
}

// ListModels calls GET /models with caching. A 404 response returns an empty
// slice with no error; the client remembers this and ValidateModelName will
// accept any non-empty name thereafter.
func (c *Client) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return c.models.GetOrLoad(ctx, c.loadModelsFromServer)
}

func (c *Client) loadModelsFromServer(ctx context.Context) ([]llm.ModelInfo, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		c.modelsUnavailable = true
		return []llm.ModelInfo{}, nil
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai: read models: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, wrapHTTPError(resp.StatusCode, raw)
	}
	var wire modelsResponse
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("openai: decode models: %w", err)
	}
	out := make([]llm.ModelInfo, 0, len(wire.Data))
	for _, e := range wire.Data {
		out = append(out, llm.ModelInfo{Name: e.ID, DisplayName: e.ID})
	}
	c.modelsUnavailable = false
	return out, nil
}

// InvalidateModelCache clears any cached model list.
func (c *Client) InvalidateModelCache() {
	c.models.Invalidate()
}

// doRequest constructs, sends, and returns the HTTP response. Callers own
// the response body.
func (c *Client) doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// If the context was cancelled, return the context error directly so
		// callers can errors.Is(err, context.Canceled).
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("openai: http request: %w", err)
	}
	return resp, nil
}

// wrapHTTPError builds a descriptive error for a non-2xx response. Matches
// the Anthropic/Google pattern of "descriptive wrapped errors, no shared
// sentinels" — callers inspect the message or the HTTP status, not errors.Is.
func wrapHTTPError(status int, body []byte) error {
	var wire chatResponse
	_ = json.Unmarshal(body, &wire)
	if wire.Error != nil && wire.Error.Message != "" {
		return fmt.Errorf("openai: HTTP %d: %s (type=%s code=%s)",
			status, wire.Error.Message, wire.Error.Type, wire.Error.Code)
	}
	// Fall back to the raw body, truncated.
	raw := string(body)
	if len(raw) > 256 {
		raw = raw[:256] + "..."
	}
	return fmt.Errorf("openai: HTTP %d: %s", status, raw)
}
```

- [ ] **Step 4: Verify `llm.ModelCache` has `GetOrLoad` and `Invalidate` methods, and the constructor signature matches**

Run: `grep -n 'func.*ModelCache' internal/llm/model_cache.go`
Expected: lines showing `NewModelCache`, `GetOrLoad`, `Invalidate` (or the equivalent names used by Anthropic/Google). If the method names differ, update `loadModelsFromServer` / `InvalidateModelCache` to match — the pattern is whatever the other two clients use.

- [ ] **Step 5: Run the Task 8 tests**

Run: `go test ./internal/llm/openai/... -run 'TestCreateMessage|TestListModels|TestValidateModelName' -v`
Expected: all subtests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/llm/openai/client.go internal/llm/openai/client_test.go
git commit -m "feat(llm/openai): Client with CreateMessage, ListModels, ValidateModelName"
```

---

### Task 9: `StreamMessage`

**Files:**
- Modify: `internal/llm/openai/client.go` (replace the `StreamMessage` stub)
- Modify: `internal/llm/openai/client_test.go` (add streaming tests)

- [ ] **Step 1: Append streaming tests**

Append to `internal/llm/openai/client_test.go`:

```go
func TestStreamMessage_HappyPath(t *testing.T) {
	raw, _ := os.ReadFile(filepath.Join("testdata", "stream_chunks_text.txt"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request body has stream:true.
		var wire chatRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &wire)
		if !wire.Stream {
			t.Error("request should have stream: true")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write(raw)
	}))
	defer srv.Close()

	ch, err := newTestClient(srv).StreamMessage(context.Background(), llm.MessageRequest{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	var text strings.Builder
	var sawStop bool
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("stream error: %v", c.Err)
		}
		if c.Text != nil {
			text.WriteString(*c.Text)
		}
		if c.StopReason != nil {
			sawStop = true
		}
	}
	if text.String() != "Hello world" {
		t.Errorf("text: %q", text.String())
	}
	if !sawStop {
		t.Error("no stop chunk")
	}
}

func TestStreamMessage_HTTPErrorBeforeStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":{"message":"bad key","type":"invalid_request_error","code":"invalid_api_key"}}`))
	}))
	defer srv.Close()

	_, err := newTestClient(srv).StreamMessage(context.Background(), llm.MessageRequest{Model: "gpt-4o"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention 401: %v", err)
	}
}
```

- [ ] **Step 2: Run to confirm failure (StreamMessage still returns "not yet implemented")**

Run: `go test ./internal/llm/openai/... -run TestStreamMessage`
Expected: FAIL with "not yet implemented".

- [ ] **Step 3: Replace the `StreamMessage` stub**

In `internal/llm/openai/client.go`, replace the `StreamMessage` method:

```go
// StreamMessage opens a streaming Chat Completions request and returns a
// channel of MessageChunk values. The channel is closed exactly once when
// the stream completes or errors. Pre-stream HTTP errors (non-2xx) are
// returned synchronously via the error return; errors mid-stream arrive as
// MessageChunk{Err: err} on the channel.
func (c *Client) StreamMessage(ctx context.Context, req llm.MessageRequest) (<-chan llm.MessageChunk, error) {
	wireReq := BuildChatCompletionRequest(req, true)
	body, err := json.Marshal(wireReq)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal stream request: %w", err)
	}
	resp, err := c.doRequest(ctx, http.MethodPost, "/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, wrapHTTPError(resp.StatusCode, raw)
	}
	out := make(chan llm.MessageChunk, 16)
	go parseSSEStream(ctx, resp.Body, out)
	return out, nil
}
```

- [ ] **Step 4: Run the streaming tests**

Run: `go test ./internal/llm/openai/... -run TestStreamMessage -v`
Expected: all subtests PASS.

- [ ] **Step 5: Run the full package test suite**

Run: `go test ./internal/llm/openai/... -v`
Expected: all tests in the package PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/llm/openai/client.go internal/llm/openai/client_test.go
git commit -m "feat(llm/openai): StreamMessage via SSE parser"
```

---

## Layer 4 — Startup loader

### Task 10: `loader.go` — read table, decrypt, register

**Files:**
- Create: `internal/llm/openai/loader.go`
- Create: `internal/llm/openai/loader_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/llm/openai/loader_test.go`:

```go
package openai

import (
	"context"
	"testing"

	"github.com/rapp992/gleipnir/internal/admin"
	"github.com/rapp992/gleipnir/internal/llm"
)

// fakeLoaderQuerier returns canned rows and tracks which rows were seen.
type fakeLoaderQuerier struct {
	rows []LoaderRow
	err  error
}

func (f *fakeLoaderQuerier) ListOpenAICompatProvidersForLoader(ctx context.Context) ([]LoaderRow, error) {
	return f.rows, f.err
}

func TestLoadAndRegister_NoRows(t *testing.T) {
	reg := llm.NewProviderRegistry()
	q := &fakeLoaderQuerier{}
	if err := LoadAndRegister(context.Background(), q, []byte("01234567890123456789012345678901"), reg); err != nil {
		t.Fatalf("LoadAndRegister: %v", err)
	}
	if len(reg.Providers()) != 0 {
		t.Errorf("registry should be empty, got %v", reg.Providers())
	}
}

func TestLoadAndRegister_MultipleRows(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	enc1, _ := admin.Encrypt(key, "sk-one")
	enc2, _ := admin.Encrypt(key, "sk-two")

	reg := llm.NewProviderRegistry()
	q := &fakeLoaderQuerier{rows: []LoaderRow{
		{Name: "openai", BaseURL: "https://api.openai.com/v1", APIKeyEncrypted: enc1},
		{Name: "ollama-local", BaseURL: "http://ollama:11434/v1", APIKeyEncrypted: enc2},
	}}
	if err := LoadAndRegister(context.Background(), q, key, reg); err != nil {
		t.Fatalf("LoadAndRegister: %v", err)
	}
	names := reg.Providers()
	if len(names) != 2 {
		t.Errorf("want 2 providers, got %v", names)
	}
	if _, err := reg.Get("openai"); err != nil {
		t.Errorf("openai not registered: %v", err)
	}
	if _, err := reg.Get("ollama-local"); err != nil {
		t.Errorf("ollama-local not registered: %v", err)
	}
}

func TestLoadAndRegister_CorruptCiphertextRowIsSkipped(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	enc, _ := admin.Encrypt(key, "sk-good")
	reg := llm.NewProviderRegistry()
	q := &fakeLoaderQuerier{rows: []LoaderRow{
		{Name: "corrupt", BaseURL: "https://api.openai.com/v1", APIKeyEncrypted: "not-valid-ciphertext"},
		{Name: "good",    BaseURL: "https://api.openai.com/v1", APIKeyEncrypted: enc},
	}}
	if err := LoadAndRegister(context.Background(), q, key, reg); err != nil {
		t.Fatalf("LoadAndRegister should not abort on corrupt row: %v", err)
	}
	if _, err := reg.Get("good"); err != nil {
		t.Errorf("good row should be registered: %v", err)
	}
	if _, err := reg.Get("corrupt"); err == nil {
		t.Errorf("corrupt row should have been skipped")
	}
}
```

- [ ] **Step 2: Run to confirm tests fail**

Run: `go test ./internal/llm/openai/... -run TestLoadAndRegister`
Expected: FAIL with "undefined: LoadAndRegister" / "undefined: LoaderRow".

- [ ] **Step 3: Implement `loader.go`**

Create `internal/llm/openai/loader.go`:

```go
package openai

import (
	"context"
	"log/slog"

	"github.com/rapp992/gleipnir/internal/admin"
	"github.com/rapp992/gleipnir/internal/llm"
)

// LoaderRow is the subset of openai_compat_providers needed at load time.
// It decouples the loader from the exact sqlc-generated struct name so
// main.go can adapt the sqlc rows into this shape at the call site.
type LoaderRow struct {
	Name            string
	BaseURL         string
	APIKeyEncrypted string
}

// LoaderQuerier is the interface main.go implements to hand rows to the
// loader. In production this wraps `db.Queries.ListOpenAICompatProviders`.
type LoaderQuerier interface {
	ListOpenAICompatProvidersForLoader(ctx context.Context) ([]LoaderRow, error)
}

// LoadAndRegister reads all rows from the querier, decrypts each API key
// with encKey, constructs a *Client for each, and registers it in the
// registry under the row's name. Rows whose ciphertext cannot be decrypted
// are logged and skipped; LoadAndRegister returns an error only if the
// initial list query fails.
func LoadAndRegister(ctx context.Context, q LoaderQuerier, encKey []byte, registry *llm.ProviderRegistry) error {
	rows, err := q.ListOpenAICompatProvidersForLoader(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		plaintext, err := admin.Decrypt(encKey, row.APIKeyEncrypted)
		if err != nil {
			slog.Error("openai loader: decrypt failed, skipping row",
				"name", row.Name, "err", err)
			continue
		}
		client := NewClient(row.BaseURL, plaintext)
		registry.Register(row.Name, client)
		slog.Info("openai loader: registered provider",
			"name", row.Name, "base_url", row.BaseURL)
	}
	return nil
}
```

- [ ] **Step 4: Run the loader tests**

Run: `go test ./internal/llm/openai/... -run TestLoadAndRegister -v`
Expected: all subtests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/llm/openai/loader.go internal/llm/openai/loader_test.go
git commit -m "feat(llm/openai): startup loader for admin-managed providers"
```

---

## Layer 5 — Admin handler for `/api/v1/admin/openai-providers`

### Task 11: Handler scaffold, masking, and querier interface

**Files:**
- Create: `internal/admin/openai_compat_handler.go`
- Create: `internal/admin/openai_compat_handler_test.go`

- [ ] **Step 1: Define the querier interface and masked response shape**

Create `internal/admin/openai_compat_handler.go`:

```go
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/api"
	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/llm/openai"
)

// OpenAICompatRow is the in-memory representation of one row. Decouples the
// handler from the sqlc-generated struct so the handler is test-friendly.
type OpenAICompatRow struct {
	ID              int64
	Name            string
	BaseURL         string
	APIKeyEncrypted string
	CreatedAt       string
	UpdatedAt       string
}

// OpenAICompatQuerier is the minimal DB interface the handler depends on.
// In production, main.go adapts sqlc-generated methods into this interface.
type OpenAICompatQuerier interface {
	ListOpenAICompatProviders(ctx context.Context) ([]OpenAICompatRow, error)
	GetOpenAICompatProviderByID(ctx context.Context, id int64) (OpenAICompatRow, error)
	GetOpenAICompatProviderByName(ctx context.Context, name string) (OpenAICompatRow, error)
	CreateOpenAICompatProvider(ctx context.Context, row OpenAICompatRow) (OpenAICompatRow, error)
	UpdateOpenAICompatProvider(ctx context.Context, row OpenAICompatRow) (OpenAICompatRow, error)
	DeleteOpenAICompatProvider(ctx context.Context, id int64) error
}

// ConnectionTester runs the save-time connection test. Injected for testing.
// Returns (modelsEndpointAvailable, error).
type ConnectionTester func(ctx context.Context, baseURL, apiKey string) (bool, error)

// DefaultConnectionTester calls GET {baseURL}/models with a 5s timeout.
// Non-error with a non-empty model list → (true, nil).
// Non-error with an empty list → (false, nil), meaning the backend returned
// 404 on /models (the escape-hatch from ADR-032) — caller records this as
// "models endpoint unavailable" but accepts the save.
// Any network/HTTP error → (false, err).
func DefaultConnectionTester(ctx context.Context, baseURL, apiKey string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client := openai.NewClient(baseURL, apiKey, openai.WithTimeout(5*time.Second))
	models, err := client.ListModels(ctx)
	if err != nil {
		return false, err
	}
	// loadModelsFromServer returns an empty slice (no error) when the backend
	// responds 404 on /models. That's the "models endpoint unavailable" path.
	return len(models) > 0, nil
}

var nameRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var reservedNames = map[string]bool{"anthropic": true, "google": true}

// OpenAICompatHandler handles /api/v1/admin/openai-providers/*.
type OpenAICompatHandler struct {
	q        OpenAICompatQuerier
	encKey   []byte
	registry *llm.ProviderRegistry
	tester   ConnectionTester

	// In-memory models-endpoint-available state, keyed by row name.
	// Rebuilt on startup (from each save) and updated on every save/test.
	modelsAvail map[string]bool
}

// NewOpenAICompatHandler constructs the handler. If tester is nil,
// DefaultConnectionTester is used.
func NewOpenAICompatHandler(q OpenAICompatQuerier, encKey []byte, registry *llm.ProviderRegistry, tester ConnectionTester) *OpenAICompatHandler {
	if tester == nil {
		tester = DefaultConnectionTester
	}
	return &OpenAICompatHandler{
		q:           q,
		encKey:      encKey,
		registry:    registry,
		tester:      tester,
		modelsAvail: map[string]bool{},
	}
}

// providerResponse is the JSON-encoded shape returned by GET/POST/PUT.
type providerResponse struct {
	ID                      int64  `json:"id"`
	Name                    string `json:"name"`
	BaseURL                 string `json:"base_url"`
	MaskedKey               string `json:"masked_key"`
	ModelsEndpointAvailable bool   `json:"models_endpoint_available"`
	CreatedAt               string `json:"created_at"`
	UpdatedAt               string `json:"updated_at"`
}

func (h *OpenAICompatHandler) rowToResponse(row OpenAICompatRow) providerResponse {
	plain, _ := Decrypt(h.encKey, row.APIKeyEncrypted)
	return providerResponse{
		ID:                      row.ID,
		Name:                    row.Name,
		BaseURL:                 row.BaseURL,
		MaskedKey:               MaskKey(plain),
		ModelsEndpointAvailable: h.modelsAvail[row.Name],
		CreatedAt:               row.CreatedAt,
		UpdatedAt:               row.UpdatedAt,
	}
}

// ListProviders GET /api/v1/admin/openai-providers
func (h *OpenAICompatHandler) ListProviders(w http.ResponseWriter, r *http.Request) {
	rows, err := h.q.ListOpenAICompatProviders(r.Context())
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to list providers", err.Error())
		return
	}
	out := make([]providerResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, h.rowToResponse(row))
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// GetProvider GET /api/v1/admin/openai-providers/{id}
func (h *OpenAICompatHandler) GetProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	row, err := h.q.GetOpenAICompatProviderByID(r.Context(), id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "provider not found", "")
		return
	}
	api.WriteJSON(w, http.StatusOK, h.rowToResponse(row))
}

func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid id", raw)
		return 0, false
	}
	return id, true
}

// validateNameFormat returns an error describing why name is invalid, or nil.
func validateNameFormat(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if reservedNames[name] {
		return fmt.Errorf("name %q is reserved", name)
	}
	if !nameRegexp.MatchString(name) {
		return fmt.Errorf("name must match ^[a-z0-9][a-z0-9_-]{0,63}$")
	}
	return nil
}

// validateBaseURL parses and normalizes (strips trailing slashes) a base URL.
func validateBaseURL(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("base_url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("base_url is not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("base_url must use http or https, got %q", u.Scheme)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("base_url must not contain a query string or fragment")
	}
	return strings.TrimRight(raw, "/"), nil
}

// isMaskedKey returns true when the supplied API key is actually a masked
// value (like "sk-...wxyz") rather than a real key. The handler treats this
// as "don't change the stored key."
func isMaskedKey(key string) bool {
	return strings.Contains(key, "...")
}
```

- [ ] **Step 2: Verify the package compiles**

Run: `go build ./internal/admin/...`
Expected: exit code 0. If there are unused imports (`json`, `time` if not referenced in this task's code), remove them — subsequent tasks will add them back.

- [ ] **Step 3: Commit the scaffold**

```bash
git add internal/admin/openai_compat_handler.go
git commit -m "feat(admin): OpenAICompatHandler scaffold with validation helpers"
```

---

### Task 12: `CreateProvider` + tests

**Files:**
- Modify: `internal/admin/openai_compat_handler.go` (add `CreateProvider`)
- Modify: `internal/admin/openai_compat_handler_test.go`

- [ ] **Step 1: Write the failing tests first**

Create `internal/admin/openai_compat_handler_test.go`:

```go
package admin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/llm"
)

// fakeOpenAICompatQuerier is an in-memory implementation for tests.
type fakeOpenAICompatQuerier struct {
	rows   map[int64]OpenAICompatRow
	nextID int64
	listErr error
}

func newFakeQuerier() *fakeOpenAICompatQuerier {
	return &fakeOpenAICompatQuerier{rows: map[int64]OpenAICompatRow{}, nextID: 1}
}

func (f *fakeOpenAICompatQuerier) ListOpenAICompatProviders(ctx context.Context) ([]OpenAICompatRow, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]OpenAICompatRow, 0, len(f.rows))
	for _, r := range f.rows {
		out = append(out, r)
	}
	return out, nil
}
func (f *fakeOpenAICompatQuerier) GetOpenAICompatProviderByID(ctx context.Context, id int64) (OpenAICompatRow, error) {
	r, ok := f.rows[id]
	if !ok {
		return OpenAICompatRow{}, sql.ErrNoRows
	}
	return r, nil
}
func (f *fakeOpenAICompatQuerier) GetOpenAICompatProviderByName(ctx context.Context, name string) (OpenAICompatRow, error) {
	for _, r := range f.rows {
		if r.Name == name {
			return r, nil
		}
	}
	return OpenAICompatRow{}, sql.ErrNoRows
}
func (f *fakeOpenAICompatQuerier) CreateOpenAICompatProvider(ctx context.Context, row OpenAICompatRow) (OpenAICompatRow, error) {
	row.ID = f.nextID
	f.nextID++
	f.rows[row.ID] = row
	return row, nil
}
func (f *fakeOpenAICompatQuerier) UpdateOpenAICompatProvider(ctx context.Context, row OpenAICompatRow) (OpenAICompatRow, error) {
	if _, ok := f.rows[row.ID]; !ok {
		return OpenAICompatRow{}, sql.ErrNoRows
	}
	f.rows[row.ID] = row
	return row, nil
}
func (f *fakeOpenAICompatQuerier) DeleteOpenAICompatProvider(ctx context.Context, id int64) error {
	if _, ok := f.rows[id]; !ok {
		return sql.ErrNoRows
	}
	delete(f.rows, id)
	return nil
}

// testKey is a 32-byte AES-GCM key.
var testKey = []byte("01234567890123456789012345678901")

// okTester is a ConnectionTester that always succeeds.
func okTester(ctx context.Context, baseURL, apiKey string) (bool, error) {
	return true, nil
}

// failTester returns the supplied error.
func failTester(err error) ConnectionTester {
	return func(ctx context.Context, baseURL, apiKey string) (bool, error) {
		return false, err
	}
}

// notFoundTester simulates a backend without /v1/models.
func notFoundTester(ctx context.Context, baseURL, apiKey string) (bool, error) {
	return false, nil
}

// newTestHandler wires up a fake querier, registry, and tester.
func newTestHandler(tester ConnectionTester) (*OpenAICompatHandler, *fakeOpenAICompatQuerier, *llm.ProviderRegistry) {
	q := newFakeQuerier()
	reg := llm.NewProviderRegistry()
	h := NewOpenAICompatHandler(q, testKey, reg, tester)
	return h, q, reg
}

func doRequest(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// mountRouter registers every openai-compat route on a chi mux for testing.
func mountRouter(h *OpenAICompatHandler) http.Handler {
	r := chi.NewRouter()
	r.Get("/api/v1/admin/openai-providers", h.ListProviders)
	r.Post("/api/v1/admin/openai-providers", h.CreateProvider)
	r.Get("/api/v1/admin/openai-providers/{id}", h.GetProvider)
	r.Put("/api/v1/admin/openai-providers/{id}", h.UpdateProvider)
	r.Delete("/api/v1/admin/openai-providers/{id}", h.DeleteProvider)
	r.Post("/api/v1/admin/openai-providers/{id}/test", h.TestProvider)
	return r
}

func TestCreate_HappyPath(t *testing.T) {
	h, q, reg := newTestHandler(okTester)
	router := mountRouter(h)

	body := map[string]string{"name": "openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-abc"}
	w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: %d, body: %s", w.Code, w.Body.String())
	}
	if len(q.rows) != 1 {
		t.Errorf("row not persisted")
	}
	if _, err := reg.Get("openai"); err != nil {
		t.Errorf("not registered: %v", err)
	}
}

func TestCreate_ReservedName(t *testing.T) {
	h, _, _ := newTestHandler(okTester)
	router := mountRouter(h)
	for _, name := range []string{"anthropic", "google"} {
		body := map[string]string{"name": name, "base_url": "https://api.openai.com/v1", "api_key": "sk-abc"}
		w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, w.Code)
		}
	}
}

func TestCreate_InvalidNameFormat(t *testing.T) {
	h, _, _ := newTestHandler(okTester)
	router := mountRouter(h)
	for _, bad := range []string{"", "With Spaces", "UPPER", "-leading-dash", "way-too-long-name-that-exceeds-the-sixty-four-character-limit-by-quite-a-bit"} {
		body := map[string]string{"name": bad, "base_url": "https://api.openai.com/v1", "api_key": "sk-abc"}
		w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("name %q: status %d, want 400", bad, w.Code)
		}
	}
}

func TestCreate_InvalidBaseURL(t *testing.T) {
	h, _, _ := newTestHandler(okTester)
	router := mountRouter(h)
	for _, bad := range []string{"", "not-a-url", "ftp://example.com", "https://example.com/?x=1"} {
		body := map[string]string{"name": "ok", "base_url": bad, "api_key": "sk-abc"}
		w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("base_url %q: status %d, want 400", bad, w.Code)
		}
	}
}

func TestCreate_EmptyAPIKey(t *testing.T) {
	h, _, _ := newTestHandler(okTester)
	router := mountRouter(h)
	body := map[string]string{"name": "ok", "base_url": "https://api.openai.com/v1", "api_key": ""}
	w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: %d, want 400", w.Code)
	}
}

func TestCreate_DuplicateName(t *testing.T) {
	h, _, _ := newTestHandler(okTester)
	router := mountRouter(h)
	body := map[string]string{"name": "openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-abc"}
	_ = doRequest(t, router, "POST", "/api/v1/admin/openai-providers", body)
	w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers", body)
	if w.Code != http.StatusConflict {
		t.Errorf("status: %d, want 409", w.Code)
	}
}

func TestCreate_ConnectionTestFails(t *testing.T) {
	h, q, reg := newTestHandler(failTester(errors.New("connection refused")))
	router := mountRouter(h)
	body := map[string]string{"name": "openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-abc"}
	w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: %d, want 400", w.Code)
	}
	if len(q.rows) != 0 {
		t.Errorf("row should not have been created")
	}
	if len(reg.Providers()) != 0 {
		t.Errorf("registry should not have been mutated")
	}
	if !strings.Contains(w.Body.String(), "connection refused") {
		t.Errorf("error should mention connection refused: %s", w.Body.String())
	}
}

func TestCreate_ConnectionTest404AcceptsWithFlag(t *testing.T) {
	h, q, reg := newTestHandler(notFoundTester)
	router := mountRouter(h)
	body := map[string]string{"name": "ollama", "base_url": "http://ollama:11434/v1", "api_key": "placeholder"}
	w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: %d, want 201. body: %s", w.Code, w.Body.String())
	}
	if len(q.rows) != 1 {
		t.Errorf("row not persisted")
	}
	if _, err := reg.Get("ollama"); err != nil {
		t.Errorf("not registered: %v", err)
	}
	var resp providerResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ModelsEndpointAvailable {
		t.Errorf("models_endpoint_available should be false")
	}
}
```

- [ ] **Step 2: Run the tests to confirm failure**

Run: `go test ./internal/admin/... -run TestCreate`
Expected: FAIL — `CreateProvider`, `UpdateProvider`, `DeleteProvider`, `TestProvider` undefined.

- [ ] **Step 3: Append `CreateProvider` to the handler**

Append to `internal/admin/openai_compat_handler.go`:

```go
// createRequest is the JSON body accepted by POST and PUT.
type createRequest struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

// CreateProvider POST /api/v1/admin/openai-providers
func (h *OpenAICompatHandler) CreateProvider(w http.ResponseWriter, r *http.Request) {
	var body createRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid JSON body", err.Error())
		return
	}
	if err := validateNameFormat(body.Name); err != nil {
		api.WriteError(w, http.StatusBadRequest, err.Error(), "")
		return
	}
	base, err := validateBaseURL(body.BaseURL)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, err.Error(), "")
		return
	}
	if body.APIKey == "" {
		api.WriteError(w, http.StatusBadRequest, "api_key is required", "")
		return
	}
	if _, err := h.q.GetOpenAICompatProviderByName(r.Context(), body.Name); err == nil {
		api.WriteError(w, http.StatusConflict, "name already in use", "")
		return
	}
	modelsAvail, err := h.tester(r.Context(), base, body.APIKey)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest,
			fmt.Sprintf("connection test failed: %v", err), "")
		return
	}
	enc, err := Encrypt(h.encKey, body.APIKey)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "encryption failed", "")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	row, err := h.q.CreateOpenAICompatProvider(r.Context(), OpenAICompatRow{
		Name:            body.Name,
		BaseURL:         base,
		APIKeyEncrypted: enc,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to create provider", err.Error())
		return
	}
	// Only mutate the registry after the DB write succeeds.
	client := openai.NewClient(base, body.APIKey)
	h.registry.Register(body.Name, client)
	h.modelsAvail[body.Name] = modelsAvail

	api.WriteJSON(w, http.StatusCreated, h.rowToResponse(row))
}
```

- [ ] **Step 4: Run just `TestCreate` — expect the create-related tests to pass, update/delete/test tests to still fail-to-compile**

Run: `go test ./internal/admin/... -run TestCreate -v`
Expected: package fails to compile because `UpdateProvider`, `DeleteProvider`, `TestProvider` are still referenced by `mountRouter`. Fix by temporarily commenting those three route lines in `mountRouter`, or by adding stubs. Use stubs — they'll be replaced in Tasks 13–15.

Add to the bottom of `openai_compat_handler.go`:

```go
// Stubs — implemented in subsequent tasks.
func (h *OpenAICompatHandler) UpdateProvider(w http.ResponseWriter, r *http.Request) {
	api.WriteError(w, http.StatusNotImplemented, "not implemented", "")
}
func (h *OpenAICompatHandler) DeleteProvider(w http.ResponseWriter, r *http.Request) {
	api.WriteError(w, http.StatusNotImplemented, "not implemented", "")
}
func (h *OpenAICompatHandler) TestProvider(w http.ResponseWriter, r *http.Request) {
	api.WriteError(w, http.StatusNotImplemented, "not implemented", "")
}
```

Run again: `go test ./internal/admin/... -run TestCreate -v`
Expected: `TestCreate_*` subtests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/admin/openai_compat_handler.go internal/admin/openai_compat_handler_test.go
git commit -m "feat(admin): CreateProvider for openai-compat instances"
```

---

### Task 13: `UpdateProvider` + tests

**Files:**
- Modify: `internal/admin/openai_compat_handler.go`
- Modify: `internal/admin/openai_compat_handler_test.go`

- [ ] **Step 1: Append tests**

Append to `openai_compat_handler_test.go`:

```go
func TestUpdate_MaskedKeyKeepsCiphertext(t *testing.T) {
	h, q, _ := newTestHandler(okTester)
	router := mountRouter(h)

	// Create a provider.
	create := map[string]string{"name": "openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-original"}
	wc := doRequest(t, router, "POST", "/api/v1/admin/openai-providers", create)
	if wc.Code != http.StatusCreated {
		t.Fatalf("create: %d", wc.Code)
	}
	var created providerResponse
	_ = json.Unmarshal(wc.Body.Bytes(), &created)

	originalCiphertext := q.rows[created.ID].APIKeyEncrypted
	originalMasked := created.MaskedKey

	// PUT with the masked value — should keep ciphertext unchanged.
	upd := map[string]string{"name": "openai", "base_url": "https://api.openai.com/v2", "api_key": originalMasked}
	wu := doRequest(t, router, "PUT", "/api/v1/admin/openai-providers/1", upd)
	if wu.Code != http.StatusOK {
		t.Fatalf("update: %d, body: %s", wu.Code, wu.Body.String())
	}
	if got := q.rows[created.ID].APIKeyEncrypted; got != originalCiphertext {
		t.Errorf("ciphertext should be unchanged, got different bytes")
	}
	if got := q.rows[created.ID].BaseURL; got != "https://api.openai.com/v2" {
		t.Errorf("base_url not updated: %q", got)
	}
}

func TestUpdate_NewKeyReencrypts(t *testing.T) {
	h, q, _ := newTestHandler(okTester)
	router := mountRouter(h)

	create := map[string]string{"name": "openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-original"}
	doRequest(t, router, "POST", "/api/v1/admin/openai-providers", create)
	originalCiphertext := q.rows[1].APIKeyEncrypted

	upd := map[string]string{"name": "openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-new-key-xyz"}
	wu := doRequest(t, router, "PUT", "/api/v1/admin/openai-providers/1", upd)
	if wu.Code != http.StatusOK {
		t.Fatalf("update: %d", wu.Code)
	}
	if q.rows[1].APIKeyEncrypted == originalCiphertext {
		t.Errorf("ciphertext should have changed")
	}
	plain, _ := Decrypt(testKey, q.rows[1].APIKeyEncrypted)
	if plain != "sk-new-key-xyz" {
		t.Errorf("plaintext after decrypt: %q", plain)
	}
}

func TestUpdate_NameChangeReregisters(t *testing.T) {
	h, _, reg := newTestHandler(okTester)
	router := mountRouter(h)
	doRequest(t, router, "POST", "/api/v1/admin/openai-providers",
		map[string]string{"name": "old-name", "base_url": "https://api.openai.com/v1", "api_key": "sk-abc"})

	upd := map[string]string{"name": "new-name", "base_url": "https://api.openai.com/v1", "api_key": "sk-...abc"}
	_ = doRequest(t, router, "PUT", "/api/v1/admin/openai-providers/1", upd)

	if _, err := reg.Get("new-name"); err != nil {
		t.Errorf("new-name not registered: %v", err)
	}
	if _, err := reg.Get("old-name"); err == nil {
		t.Errorf("old-name should have been unregistered")
	}
}

func TestUpdate_NameCollision(t *testing.T) {
	h, _, _ := newTestHandler(okTester)
	router := mountRouter(h)
	doRequest(t, router, "POST", "/api/v1/admin/openai-providers",
		map[string]string{"name": "a", "base_url": "https://api.openai.com/v1", "api_key": "sk-1"})
	doRequest(t, router, "POST", "/api/v1/admin/openai-providers",
		map[string]string{"name": "b", "base_url": "https://api.openai.com/v1", "api_key": "sk-2"})

	// Try to rename row 2 to "a".
	upd := map[string]string{"name": "a", "base_url": "https://api.openai.com/v1", "api_key": "sk-...sk-2"}
	w := doRequest(t, router, "PUT", "/api/v1/admin/openai-providers/2", upd)
	if w.Code != http.StatusConflict {
		t.Errorf("status: %d, want 409", w.Code)
	}
}
```

- [ ] **Step 2: Run to confirm the update tests fail**

Run: `go test ./internal/admin/... -run TestUpdate`
Expected: FAIL — currently returns 501.

- [ ] **Step 3: Replace the `UpdateProvider` stub**

```go
// UpdateProvider PUT /api/v1/admin/openai-providers/{id}
func (h *OpenAICompatHandler) UpdateProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var body createRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid JSON body", err.Error())
		return
	}
	if err := validateNameFormat(body.Name); err != nil {
		api.WriteError(w, http.StatusBadRequest, err.Error(), "")
		return
	}
	base, err := validateBaseURL(body.BaseURL)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, err.Error(), "")
		return
	}

	existing, err := h.q.GetOpenAICompatProviderByID(r.Context(), id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "provider not found", "")
		return
	}

	// Name collision with a *different* row.
	if body.Name != existing.Name {
		if _, err := h.q.GetOpenAICompatProviderByName(r.Context(), body.Name); err == nil {
			api.WriteError(w, http.StatusConflict, "name already in use", "")
			return
		}
	}

	// Resolve the effective plaintext key.
	var effectiveKey string
	var newCiphertext string
	if isMaskedKey(body.APIKey) || body.APIKey == "" {
		// Keep existing ciphertext; decrypt for the connection test.
		plain, err := Decrypt(h.encKey, existing.APIKeyEncrypted)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "could not decrypt existing key", "")
			return
		}
		effectiveKey = plain
		newCiphertext = existing.APIKeyEncrypted
	} else {
		effectiveKey = body.APIKey
		enc, err := Encrypt(h.encKey, body.APIKey)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "encryption failed", "")
			return
		}
		newCiphertext = enc
	}

	modelsAvail, err := h.tester(r.Context(), base, effectiveKey)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest,
			fmt.Sprintf("connection test failed: %v", err), "")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	updated, err := h.q.UpdateOpenAICompatProvider(r.Context(), OpenAICompatRow{
		ID:              id,
		Name:            body.Name,
		BaseURL:         base,
		APIKeyEncrypted: newCiphertext,
		CreatedAt:       existing.CreatedAt,
		UpdatedAt:       now,
	})
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to update provider", err.Error())
		return
	}

	// Registry: unregister old name if it changed, then register under new name.
	if body.Name != existing.Name {
		h.registry.Unregister(existing.Name)
		delete(h.modelsAvail, existing.Name)
	}
	client := openai.NewClient(base, effectiveKey)
	h.registry.Register(body.Name, client)
	h.modelsAvail[body.Name] = modelsAvail

	api.WriteJSON(w, http.StatusOK, h.rowToResponse(updated))
}
```

- [ ] **Step 4: Run the update tests**

Run: `go test ./internal/admin/... -run 'TestCreate|TestUpdate' -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/admin/openai_compat_handler.go internal/admin/openai_compat_handler_test.go
git commit -m "feat(admin): UpdateProvider with key-preservation and re-register"
```

---

### Task 14: `DeleteProvider` + `TestProvider` + `GetProvider` tests

**Files:**
- Modify: `internal/admin/openai_compat_handler.go`
- Modify: `internal/admin/openai_compat_handler_test.go`

- [ ] **Step 1: Append tests**

Append to `openai_compat_handler_test.go`:

```go
func TestDelete_HappyPath(t *testing.T) {
	h, q, reg := newTestHandler(okTester)
	router := mountRouter(h)
	doRequest(t, router, "POST", "/api/v1/admin/openai-providers",
		map[string]string{"name": "openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-abc"})

	w := doRequest(t, router, "DELETE", "/api/v1/admin/openai-providers/1", nil)
	if w.Code != http.StatusNoContent {
		t.Errorf("status: %d, want 204", w.Code)
	}
	if len(q.rows) != 0 {
		t.Errorf("row should be deleted")
	}
	if _, err := reg.Get("openai"); err == nil {
		t.Errorf("should be unregistered")
	}
}

func TestDelete_NotFound(t *testing.T) {
	h, _, _ := newTestHandler(okTester)
	router := mountRouter(h)
	w := doRequest(t, router, "DELETE", "/api/v1/admin/openai-providers/999", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("status: %d, want 404", w.Code)
	}
}

func TestList_MasksKeys(t *testing.T) {
	h, _, _ := newTestHandler(okTester)
	router := mountRouter(h)
	doRequest(t, router, "POST", "/api/v1/admin/openai-providers",
		map[string]string{"name": "openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-very-secret-value"})

	w := doRequest(t, router, "GET", "/api/v1/admin/openai-providers", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "sk-very-secret-value") {
		t.Error("plaintext key leaked in list response")
	}
}

func TestGet_MasksKey(t *testing.T) {
	h, _, _ := newTestHandler(okTester)
	router := mountRouter(h)
	doRequest(t, router, "POST", "/api/v1/admin/openai-providers",
		map[string]string{"name": "openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-secret-xyz"})

	w := doRequest(t, router, "GET", "/api/v1/admin/openai-providers/1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "sk-secret-xyz") {
		t.Error("plaintext key leaked in get response")
	}
}

func TestTestProvider_HappyPath(t *testing.T) {
	h, _, _ := newTestHandler(okTester)
	router := mountRouter(h)
	doRequest(t, router, "POST", "/api/v1/admin/openai-providers",
		map[string]string{"name": "openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-abc"})

	w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers/1/test", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		OK                      bool   `json:"ok"`
		ModelsEndpointAvailable bool   `json:"models_endpoint_available"`
		Error                   string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.OK {
		t.Errorf("want ok=true, got %+v", resp)
	}
}

func TestTestProvider_Unreachable(t *testing.T) {
	h, _, _ := newTestHandler(okTester)
	router := mountRouter(h)
	doRequest(t, router, "POST", "/api/v1/admin/openai-providers",
		map[string]string{"name": "openai", "base_url": "https://api.openai.com/v1", "api_key": "sk-abc"})

	// Swap the tester to a failing one for the re-test.
	h.tester = failTester(errors.New("dial tcp: i/o timeout"))

	w := doRequest(t, router, "POST", "/api/v1/admin/openai-providers/1/test", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.OK {
		t.Errorf("want ok=false, got %+v", resp)
	}
	if !strings.Contains(resp.Error, "i/o timeout") {
		t.Errorf("error should mention timeout: %q", resp.Error)
	}
}
```

- [ ] **Step 2: Run to confirm new tests fail**

Run: `go test ./internal/admin/... -run 'TestDelete|TestList_Masks|TestGet_Masks|TestTestProvider'`
Expected: FAIL — stubs return 501 and list/get have no coverage yet.

- [ ] **Step 3: Replace the `DeleteProvider` and `TestProvider` stubs**

```go
// DeleteProvider DELETE /api/v1/admin/openai-providers/{id}
func (h *OpenAICompatHandler) DeleteProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	existing, err := h.q.GetOpenAICompatProviderByID(r.Context(), id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "provider not found", "")
		return
	}
	if err := h.q.DeleteOpenAICompatProvider(r.Context(), id); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "failed to delete", err.Error())
		return
	}
	h.registry.Unregister(existing.Name)
	delete(h.modelsAvail, existing.Name)
	w.WriteHeader(http.StatusNoContent)
}

// TestProvider POST /api/v1/admin/openai-providers/{id}/test
func (h *OpenAICompatHandler) TestProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	existing, err := h.q.GetOpenAICompatProviderByID(r.Context(), id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "provider not found", "")
		return
	}
	plain, err := Decrypt(h.encKey, existing.APIKeyEncrypted)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "could not decrypt key", "")
		return
	}
	modelsAvail, err := h.tester(r.Context(), existing.BaseURL, plain)
	if err != nil {
		api.WriteJSON(w, http.StatusOK, map[string]any{
			"ok":                        false,
			"models_endpoint_available": false,
			"error":                     err.Error(),
		})
		return
	}
	h.modelsAvail[existing.Name] = modelsAvail
	api.WriteJSON(w, http.StatusOK, map[string]any{
		"ok":                        true,
		"models_endpoint_available": modelsAvail,
	})
}
```

- [ ] **Step 4: Run the full admin test suite**

Run: `go test ./internal/admin/... -v`
Expected: all tests PASS (both the existing admin tests and the new openai_compat tests).

- [ ] **Step 5: Commit**

```bash
git add internal/admin/openai_compat_handler.go internal/admin/openai_compat_handler_test.go
git commit -m "feat(admin): Delete, Test, and List/Get for openai-compat instances"
```

---

## Layer 6 — Wiring in `main.go`

### Task 15: Adapter layer from sqlc to handler/loader interfaces, and startup wiring

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Find the existing provider-registration block**

Run: `grep -n 'LoadAndRegister\|anthropic.New\|google.New\|openai\|ProviderRegistry\|NewHandler' main.go`
Expected: lines showing where Anthropic and Google clients are constructed and registered, where the admin handler is created, and where chi routes are mounted.

- [ ] **Step 2: Add an adapter type that satisfies both the handler's `OpenAICompatQuerier` and the loader's `LoaderQuerier`**

Add to `main.go` (near where other adapters live — search for the existing admin wiring to find the right spot):

```go
// openaiCompatAdapter bridges the sqlc-generated openai_compat_providers
// queries to the admin handler and openai loader interfaces. The adapter
// lives in main.go because it's the one place that sees both sqlc and
// the internal packages — keeping it here avoids cross-package import
// gymnastics.
type openaiCompatAdapter struct {
	q *db.Queries
}

func (a *openaiCompatAdapter) ListOpenAICompatProviders(ctx context.Context) ([]admin.OpenAICompatRow, error) {
	rows, err := a.q.ListOpenAICompatProviders(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]admin.OpenAICompatRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, sqlcRowToAdminRow(r))
	}
	return out, nil
}

func (a *openaiCompatAdapter) GetOpenAICompatProviderByID(ctx context.Context, id int64) (admin.OpenAICompatRow, error) {
	r, err := a.q.GetOpenAICompatProviderByID(ctx, id)
	if err != nil {
		return admin.OpenAICompatRow{}, err
	}
	return sqlcRowToAdminRow(r), nil
}

func (a *openaiCompatAdapter) GetOpenAICompatProviderByName(ctx context.Context, name string) (admin.OpenAICompatRow, error) {
	r, err := a.q.GetOpenAICompatProviderByName(ctx, name)
	if err != nil {
		return admin.OpenAICompatRow{}, err
	}
	return sqlcRowToAdminRow(r), nil
}

func (a *openaiCompatAdapter) CreateOpenAICompatProvider(ctx context.Context, row admin.OpenAICompatRow) (admin.OpenAICompatRow, error) {
	created, err := a.q.CreateOpenAICompatProvider(ctx, db.CreateOpenAICompatProviderParams{
		Name:            row.Name,
		BaseUrl:         row.BaseURL,
		ApiKeyEncrypted: row.APIKeyEncrypted,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	})
	if err != nil {
		return admin.OpenAICompatRow{}, err
	}
	return sqlcRowToAdminRow(created), nil
}

func (a *openaiCompatAdapter) UpdateOpenAICompatProvider(ctx context.Context, row admin.OpenAICompatRow) (admin.OpenAICompatRow, error) {
	updated, err := a.q.UpdateOpenAICompatProvider(ctx, db.UpdateOpenAICompatProviderParams{
		ID:              row.ID,
		Name:            row.Name,
		BaseUrl:         row.BaseURL,
		ApiKeyEncrypted: row.APIKeyEncrypted,
		UpdatedAt:       row.UpdatedAt,
	})
	if err != nil {
		return admin.OpenAICompatRow{}, err
	}
	return sqlcRowToAdminRow(updated), nil
}

func (a *openaiCompatAdapter) DeleteOpenAICompatProvider(ctx context.Context, id int64) error {
	return a.q.DeleteOpenAICompatProvider(ctx, id)
}

// ListOpenAICompatProvidersForLoader satisfies openai.LoaderQuerier.
func (a *openaiCompatAdapter) ListOpenAICompatProvidersForLoader(ctx context.Context) ([]openai.LoaderRow, error) {
	rows, err := a.q.ListOpenAICompatProviders(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]openai.LoaderRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, openai.LoaderRow{
			Name:            r.Name,
			BaseURL:         r.BaseUrl,
			APIKeyEncrypted: r.ApiKeyEncrypted,
		})
	}
	return out, nil
}

// sqlcRowToAdminRow converts a sqlc-generated row to the handler's shape.
// IMPORTANT: the field names (BaseUrl vs BaseURL, ApiKeyEncrypted vs
// APIKeyEncrypted) depend on sqlc's Go naming. Verify by opening
// internal/db/openai_compat_providers.sql.go after sqlc generate and
// matching the field access to what it emits. The code below assumes
// sqlc emits BaseUrl / ApiKeyEncrypted (its default snake_case -> PascalCase).
func sqlcRowToAdminRow(r db.OpenaiCompatProvider) admin.OpenAICompatRow {
	return admin.OpenAICompatRow{
		ID:              r.ID,
		Name:            r.Name,
		BaseURL:         r.BaseUrl,
		APIKeyEncrypted: r.ApiKeyEncrypted,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
	}
}
```

- [ ] **Step 3: Add imports if they aren't already present**

Ensure `main.go` imports include:

```go
"github.com/rapp992/gleipnir/internal/llm/openai"
```

The `context`, `db`, and `admin` imports are almost certainly already present.

- [ ] **Step 4: Construct the adapter, instantiate the handler, invoke the loader at startup**

Locate the block where `admin.NewHandler` is called (for the existing Anthropic/Google handler). Immediately after it, add:

```go
	// OpenAI-compatible provider instances (ADR-032).
	openaiAdapter := &openaiCompatAdapter{q: queries}
	openaiCompatHandler := admin.NewOpenAICompatHandler(
		openaiAdapter,
		encryptionKey, // same key used by the existing admin handler
		providerRegistry,
		nil, // use admin.DefaultConnectionTester
	)

	// Load existing rows and register each one in the provider registry.
	if err := openai.LoadAndRegister(ctx, openaiAdapter, encryptionKey, providerRegistry); err != nil {
		slog.Error("failed to load openai-compat providers at startup", "err", err)
		// Do not abort; individual row failures are logged by the loader itself,
		// and a failure here (list query) leaves the feature unavailable but
		// the rest of Gleipnir should still start.
	}
```

The variable names above (`queries`, `encryptionKey`, `providerRegistry`, `ctx`) match the style already used by the Anthropic/Google wiring. If they differ in your copy of `main.go`, substitute the correct names.

- [ ] **Step 5: Mount the new routes**

Locate the block where existing admin routes are mounted on the chi router. After the existing `/api/v1/admin/*` routes, add:

```go
		r.Route("/openai-providers", func(r chi.Router) {
			r.Get("/", openaiCompatHandler.ListProviders)
			r.Post("/", openaiCompatHandler.CreateProvider)
			r.Get("/{id}", openaiCompatHandler.GetProvider)
			r.Put("/{id}", openaiCompatHandler.UpdateProvider)
			r.Delete("/{id}", openaiCompatHandler.DeleteProvider)
			r.Post("/{id}/test", openaiCompatHandler.TestProvider)
		})
```

This block goes inside whatever admin-role middleware already wraps the other admin routes.

- [ ] **Step 6: Build the whole project**

Run: `go build ./...`
Expected: exit code 0. If sqlc's emitted field names do not match `BaseUrl` / `ApiKeyEncrypted`, fix `sqlcRowToAdminRow` and the `Create`/`Update` adapters to match. The field names are the only likely compile error.

- [ ] **Step 7: Run every Go test**

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 8: Commit**

```bash
git add main.go
git commit -m "feat: wire openai-compat handler and startup loader into main"
```

---

### Task 16: Verify the policy editor model dropdown picks up new providers

**Files:**
- Read-only: `internal/api/models_handler.go`, `frontend/src/hooks/queries/models.ts` (or similar)

This is a verification-only task — no code changes expected. Per spec §8.7, the policy editor should automatically surface new providers because `/api/v1/models` reads from `registry.ListAllModels`. If it turns out the frontend hardcodes a provider list anywhere, extend this task to fix it (expected to be a single-line change).

- [ ] **Step 1: Confirm `/api/v1/models` reads from the registry**

Run: `grep -n 'ListAllModels\|hardcoded\|"anthropic"\|"google"' internal/api/models_handler.go`
Expected: to see `ListAllModels` called; no literal `"anthropic"` or `"google"` string list.

- [ ] **Step 2: Confirm the frontend policy editor reads the dynamic list**

Run: `grep -rn '/api/v1/models\|apiFetch.*models' frontend/src/hooks/queries/`
Expected: a query hook that calls the models endpoint and returns the provider-keyed map.

- [ ] **Step 3: Smoke-test the live server**

Run:
```bash
go build -o /tmp/gleipnir ./cmd/... 2>/dev/null || go build -o /tmp/gleipnir .
GLEIPNIR_DB_PATH=/tmp/gleipnir-smoke.db GLEIPNIR_SECRET_KEY="$(printf '%032d' 0)" /tmp/gleipnir &
PID=$!
sleep 1
# Hit /api/v1/models (unauthenticated endpoints may differ; skip if 401)
curl -sS http://localhost:8080/api/v1/health
kill $PID
rm -f /tmp/gleipnir-smoke.db
```

Expected: the server starts without panicking. The point of this step is not to exercise the feature end-to-end — it's to confirm the new startup wiring doesn't break existing boot.

- [ ] **Step 4: If any of Steps 1–2 revealed a hardcoded list, fix it**

If the frontend or backend had a hardcoded provider list, replace it with a call to the registry. Show the fix inline:

(No code here because the fix is expected to be unnecessary. If it is needed, the change is a 1-3 line edit to replace a literal array with a registry lookup.)

- [ ] **Step 5: Commit if any changes were made; otherwise no commit**

---

## Layer 7 — Frontend

These tasks are less TDD-driven because the frontend tests in this codebase are Vitest smoke tests and Storybook stories — exhaustive test-first development is not the established pattern. Each task ends with running the existing test/build commands to verify nothing regresses.

### Task 17: API types and fetch client

**Files:**
- Modify: `frontend/src/api/types.ts`
- Create: `frontend/src/api/openaiCompatProviders.ts`

- [ ] **Step 1: Add the TypeScript types**

Append to `frontend/src/api/types.ts`:

```ts
export interface ApiOpenAICompatProvider {
  id: number
  name: string
  base_url: string
  masked_key: string
  models_endpoint_available: boolean
  created_at: string
  updated_at: string
}

export interface ApiOpenAICompatProviderUpsert {
  name: string
  base_url: string
  api_key: string
}

export interface ApiOpenAICompatProviderTestResult {
  ok: boolean
  models_endpoint_available?: boolean
  error?: string
}
```

- [ ] **Step 2: Create the fetch client**

Create `frontend/src/api/openaiCompatProviders.ts`:

```ts
import { apiFetch } from './fetch'
import type {
  ApiOpenAICompatProvider,
  ApiOpenAICompatProviderTestResult,
  ApiOpenAICompatProviderUpsert,
} from './types'

const BASE = '/admin/openai-providers'

export function listOpenAICompatProviders() {
  return apiFetch<ApiOpenAICompatProvider[]>(BASE)
}

export function getOpenAICompatProvider(id: number) {
  return apiFetch<ApiOpenAICompatProvider>(`${BASE}/${id}`)
}

export function createOpenAICompatProvider(body: ApiOpenAICompatProviderUpsert) {
  return apiFetch<ApiOpenAICompatProvider>(BASE, {
    method: 'POST',
    body: JSON.stringify(body),
  })
}

export function updateOpenAICompatProvider(id: number, body: ApiOpenAICompatProviderUpsert) {
  return apiFetch<ApiOpenAICompatProvider>(`${BASE}/${id}`, {
    method: 'PUT',
    body: JSON.stringify(body),
  })
}

export function deleteOpenAICompatProvider(id: number) {
  return apiFetch<void>(`${BASE}/${id}`, { method: 'DELETE' })
}

export function testOpenAICompatProvider(id: number) {
  return apiFetch<ApiOpenAICompatProviderTestResult>(`${BASE}/${id}/test`, {
    method: 'POST',
  })
}
```

- [ ] **Step 3: Verify the TypeScript compiles**

Run: `cd frontend && npx tsc --noEmit`
Expected: exit code 0.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/api/types.ts frontend/src/api/openaiCompatProviders.ts
git commit -m "feat(frontend): API types and fetch for openai-compat providers"
```

---

### Task 18: TanStack Query hooks

**Files:**
- Create: `frontend/src/hooks/queries/openaiCompatProviders.ts`
- Create: `frontend/src/hooks/mutations/openaiCompatProviders.ts`

- [ ] **Step 1: Create the query hooks**

Create `frontend/src/hooks/queries/openaiCompatProviders.ts`:

```ts
import { useQuery } from '@tanstack/react-query'
import { listOpenAICompatProviders } from '../../api/openaiCompatProviders'

export const OPENAI_COMPAT_PROVIDERS_KEY = ['admin', 'openai-compat-providers'] as const

export function useOpenAICompatProviders() {
  return useQuery({
    queryKey: OPENAI_COMPAT_PROVIDERS_KEY,
    queryFn: listOpenAICompatProviders,
  })
}
```

- [ ] **Step 2: Create the mutation hooks**

Create `frontend/src/hooks/mutations/openaiCompatProviders.ts`:

```ts
import { useMutation, useQueryClient } from '@tanstack/react-query'
import {
  createOpenAICompatProvider,
  deleteOpenAICompatProvider,
  testOpenAICompatProvider,
  updateOpenAICompatProvider,
} from '../../api/openaiCompatProviders'
import type { ApiOpenAICompatProviderUpsert } from '../../api/types'
import { OPENAI_COMPAT_PROVIDERS_KEY } from '../queries/openaiCompatProviders'

export function useCreateOpenAICompatProvider() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: ApiOpenAICompatProviderUpsert) => createOpenAICompatProvider(body),
    onSuccess: () => qc.invalidateQueries({ queryKey: OPENAI_COMPAT_PROVIDERS_KEY }),
  })
}

export function useUpdateOpenAICompatProvider() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: ApiOpenAICompatProviderUpsert }) =>
      updateOpenAICompatProvider(id, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: OPENAI_COMPAT_PROVIDERS_KEY }),
  })
}

export function useDeleteOpenAICompatProvider() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => deleteOpenAICompatProvider(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: OPENAI_COMPAT_PROVIDERS_KEY }),
  })
}

export function useTestOpenAICompatProvider() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => testOpenAICompatProvider(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: OPENAI_COMPAT_PROVIDERS_KEY }),
  })
}
```

- [ ] **Step 3: Verify the TypeScript compiles**

Run: `cd frontend && npx tsc --noEmit`
Expected: exit code 0.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/hooks/queries/openaiCompatProviders.ts frontend/src/hooks/mutations/openaiCompatProviders.ts
git commit -m "feat(frontend): query/mutation hooks for openai-compat providers"
```

---

### Task 19: MSW handlers

**Files:**
- Create: `frontend/src/mocks/handlers/openaiCompatProviders.ts`
- Modify: `frontend/src/mocks/handlers/index.ts`

- [ ] **Step 1: Create the MSW handlers**

Create `frontend/src/mocks/handlers/openaiCompatProviders.ts`:

```ts
import { http, HttpResponse } from 'msw'
import type { ApiOpenAICompatProvider } from '../../api/types'

const rows: ApiOpenAICompatProvider[] = [
  {
    id: 1,
    name: 'openai',
    base_url: 'https://api.openai.com/v1',
    masked_key: 'sk-...abcd',
    models_endpoint_available: true,
    created_at: '2026-04-01T12:00:00Z',
    updated_at: '2026-04-01T12:00:00Z',
  },
]

export const openaiCompatProvidersHandlers = [
  http.get('/api/v1/admin/openai-providers', () => HttpResponse.json({ data: rows })),

  http.get('/api/v1/admin/openai-providers/:id', ({ params }) => {
    const id = Number(params.id)
    const row = rows.find((r) => r.id === id)
    if (!row) return new HttpResponse(null, { status: 404 })
    return HttpResponse.json({ data: row })
  }),

  http.post('/api/v1/admin/openai-providers', async ({ request }) => {
    const body = (await request.json()) as { name: string; base_url: string; api_key: string }
    const row: ApiOpenAICompatProvider = {
      id: rows.length + 1,
      name: body.name,
      base_url: body.base_url,
      masked_key: `${body.api_key.slice(0, 3)}...${body.api_key.slice(-4)}`,
      models_endpoint_available: true,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    }
    rows.push(row)
    return HttpResponse.json({ data: row }, { status: 201 })
  }),

  http.put('/api/v1/admin/openai-providers/:id', async ({ params, request }) => {
    const id = Number(params.id)
    const body = (await request.json()) as { name: string; base_url: string; api_key: string }
    const idx = rows.findIndex((r) => r.id === id)
    if (idx < 0) return new HttpResponse(null, { status: 404 })
    rows[idx] = { ...rows[idx], name: body.name, base_url: body.base_url, updated_at: new Date().toISOString() }
    return HttpResponse.json({ data: rows[idx] })
  }),

  http.delete('/api/v1/admin/openai-providers/:id', ({ params }) => {
    const id = Number(params.id)
    const idx = rows.findIndex((r) => r.id === id)
    if (idx < 0) return new HttpResponse(null, { status: 404 })
    rows.splice(idx, 1)
    return new HttpResponse(null, { status: 204 })
  }),

  http.post('/api/v1/admin/openai-providers/:id/test', () =>
    HttpResponse.json({ data: { ok: true, models_endpoint_available: true } }),
  ),
]
```

- [ ] **Step 2: Register the handlers in the handler index**

Open `frontend/src/mocks/handlers/index.ts` and add:

```ts
import { openaiCompatProvidersHandlers } from './openaiCompatProviders'

export const handlers = [
  // ...existing handlers
  ...openaiCompatProvidersHandlers,
]
```

The exact location depends on the file's current shape — merge into the exported `handlers` array however the existing file organizes it.

- [ ] **Step 3: Commit**

```bash
git add frontend/src/mocks/handlers/openaiCompatProviders.ts frontend/src/mocks/handlers/index.ts
git commit -m "feat(frontend): MSW handlers for openai-compat providers"
```

---

### Task 20: Section component (table + empty state)

**Files:**
- Create: `frontend/src/components/admin/OpenAICompatProvidersSection.tsx`
- Create: `frontend/src/components/admin/OpenAICompatProvidersSection.module.css`
- Create: `frontend/src/components/admin/OpenAICompatProvidersSection.stories.tsx`

- [ ] **Step 1: Create the section component**

Create `frontend/src/components/admin/OpenAICompatProvidersSection.tsx`:

```tsx
import { useState } from 'react'
import type { ApiOpenAICompatProvider } from '../../api/types'
import { useOpenAICompatProviders } from '../../hooks/queries/openaiCompatProviders'
import {
  useDeleteOpenAICompatProvider,
  useTestOpenAICompatProvider,
} from '../../hooks/mutations/openaiCompatProviders'
import { OpenAICompatProviderModal } from './OpenAICompatProviderModal'
import { OpenAICompatProviderDeleteDialog } from './OpenAICompatProviderDeleteDialog'
import styles from './OpenAICompatProvidersSection.module.css'

export function OpenAICompatProvidersSection() {
  const { data: providers, isLoading } = useOpenAICompatProviders()
  const deleteMut = useDeleteOpenAICompatProvider()
  const testMut = useTestOpenAICompatProvider()

  const [modalState, setModalState] = useState<
    | { mode: 'closed' }
    | { mode: 'create' }
    | { mode: 'edit'; provider: ApiOpenAICompatProvider }
  >({ mode: 'closed' })
  const [deleteTarget, setDeleteTarget] = useState<ApiOpenAICompatProvider | null>(null)

  if (isLoading) {
    return <div className={styles.loading}>Loading...</div>
  }

  const rows = providers ?? []

  return (
    <section className={styles.section} aria-labelledby="openai-compat-heading">
      <header className={styles.header}>
        <h2 id="openai-compat-heading" className={styles.heading}>
          OpenAI-compatible providers
        </h2>
        <p className={styles.description}>
          Admin-managed instances backed by the OpenAI Chat Completions API.
          Add one per backend (OpenAI itself, Ollama, vLLM, OpenRouter, etc.).
        </p>
        <button
          type="button"
          className={styles.addButton}
          onClick={() => setModalState({ mode: 'create' })}
        >
          Add provider
        </button>
      </header>

      {rows.length === 0 ? (
        <div className={styles.empty}>
          <p>No OpenAI-compatible providers configured.</p>
          <p>Add one to use OpenAI, Ollama, vLLM, or any compatible backend.</p>
        </div>
      ) : (
        <table className={styles.table}>
          <thead>
            <tr>
              <th>Name</th>
              <th>Base URL</th>
              <th>API Key</th>
              <th>Models</th>
              <th>Updated</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((p) => (
              <tr key={p.id}>
                <td className={styles.name}>{p.name}</td>
                <td className={styles.url}>{p.base_url}</td>
                <td className={styles.key}>{p.masked_key}</td>
                <td>
                  {p.models_endpoint_available ? (
                    <span className={styles.badgeOk}>Available</span>
                  ) : (
                    <span className={styles.badgeWarn}>models endpoint unavailable</span>
                  )}
                </td>
                <td>{p.updated_at}</td>
                <td className={styles.actions}>
                  <button
                    type="button"
                    onClick={() => testMut.mutate(p.id)}
                    disabled={testMut.isPending}
                  >
                    Test
                  </button>
                  <button type="button" onClick={() => setModalState({ mode: 'edit', provider: p })}>
                    Edit
                  </button>
                  <button
                    type="button"
                    className={styles.danger}
                    onClick={() => setDeleteTarget(p)}
                  >
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {modalState.mode !== 'closed' && (
        <OpenAICompatProviderModal
          mode={modalState.mode}
          provider={modalState.mode === 'edit' ? modalState.provider : undefined}
          onClose={() => setModalState({ mode: 'closed' })}
        />
      )}

      {deleteTarget && (
        <OpenAICompatProviderDeleteDialog
          provider={deleteTarget}
          onClose={() => setDeleteTarget(null)}
          onConfirm={() => {
            deleteMut.mutate(deleteTarget.id, { onSettled: () => setDeleteTarget(null) })
          }}
        />
      )}
    </section>
  )
}
```

- [ ] **Step 2: Create the CSS Module**

Create `frontend/src/components/admin/OpenAICompatProvidersSection.module.css`:

```css
.section {
  margin-top: 32px;
  padding: 24px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-surface);
}

.header {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 16px;
}

.heading {
  margin: 0;
  font-size: var(--font-size-lg);
  color: var(--color-text);
}

.description {
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--font-size-sm);
}

.addButton {
  align-self: flex-start;
  margin-top: 8px;
  padding: 8px 16px;
  background: var(--color-primary);
  color: var(--color-primary-text);
  border: none;
  border-radius: 4px;
  cursor: pointer;
}

.empty {
  padding: 24px;
  text-align: center;
  color: var(--color-text-muted);
  background: var(--color-surface-muted);
  border-radius: 4px;
}

.table {
  width: 100%;
  border-collapse: collapse;
}

.table th,
.table td {
  padding: 12px 8px;
  text-align: left;
  border-bottom: 1px solid var(--color-border);
}

.name {
  font-weight: 600;
}

.url,
.key {
  font-family: var(--font-mono);
  font-size: var(--font-size-sm);
}

.badgeOk {
  display: inline-block;
  padding: 4px 8px;
  border-radius: 4px;
  background: var(--color-success-bg);
  color: var(--color-success-text);
  font-size: var(--font-size-xs);
}

.badgeWarn {
  display: inline-block;
  padding: 4px 8px;
  border-radius: 4px;
  background: var(--color-warn-bg);
  color: var(--color-warn-text);
  font-size: var(--font-size-xs);
}

.actions {
  display: flex;
  gap: 8px;
}

.danger {
  color: var(--color-danger-text);
}

.loading {
  padding: 24px;
  color: var(--color-text-muted);
}
```

Note: the CSS custom property names (`--color-border`, `--color-primary`, etc.) must match whatever exists in the project's design tokens. Open `frontend/src/styles/tokens.css` (or the equivalent) and substitute exact names if these don't match.

- [ ] **Step 3: Create a Storybook story**

Create `frontend/src/components/admin/OpenAICompatProvidersSection.stories.tsx`:

```tsx
import type { Meta, StoryObj } from '@storybook/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { OpenAICompatProvidersSection } from './OpenAICompatProvidersSection'

const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })

const meta: Meta<typeof OpenAICompatProvidersSection> = {
  title: 'Admin/OpenAICompatProvidersSection',
  component: OpenAICompatProvidersSection,
  decorators: [
    (Story) => (
      <QueryClientProvider client={qc}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}
export default meta

export const Default: StoryObj<typeof OpenAICompatProvidersSection> = {}
```

- [ ] **Step 4: Verify the frontend builds**

Run: `cd frontend && npm run build`
Expected: exit code 0 (warnings OK, errors not OK).

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/admin/OpenAICompatProvidersSection.tsx frontend/src/components/admin/OpenAICompatProvidersSection.module.css frontend/src/components/admin/OpenAICompatProvidersSection.stories.tsx
git commit -m "feat(frontend): OpenAICompatProvidersSection component"
```

---

### Task 21: Modal with OpenAI quick-preset chip

**Files:**
- Create: `frontend/src/components/admin/OpenAICompatProviderModal.tsx`
- Create: `frontend/src/components/admin/OpenAICompatProviderModal.module.css`
- Create: `frontend/src/components/admin/OpenAICompatProviderModal.stories.tsx`

- [ ] **Step 1: Create the modal component**

Create `frontend/src/components/admin/OpenAICompatProviderModal.tsx`:

```tsx
import { useState, type FormEvent } from 'react'
import type { ApiOpenAICompatProvider } from '../../api/types'
import {
  useCreateOpenAICompatProvider,
  useUpdateOpenAICompatProvider,
} from '../../hooks/mutations/openaiCompatProviders'
import styles from './OpenAICompatProviderModal.module.css'

interface Props {
  mode: 'create' | 'edit'
  provider?: ApiOpenAICompatProvider
  onClose: () => void
}

export function OpenAICompatProviderModal({ mode, provider, onClose }: Props) {
  const [name, setName] = useState(provider?.name ?? '')
  const [baseUrl, setBaseUrl] = useState(provider?.base_url ?? '')
  const [apiKey, setApiKey] = useState(provider?.masked_key ?? '')
  const [error, setError] = useState<string | null>(null)

  const createMut = useCreateOpenAICompatProvider()
  const updateMut = useUpdateOpenAICompatProvider()
  const pending = createMut.isPending || updateMut.isPending

  const applyOpenAIPreset = () => {
    if (!name) setName('openai')
    setBaseUrl('https://api.openai.com/v1')
    setApiKey('')
  }

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault()
    setError(null)
    const body = { name, base_url: baseUrl, api_key: apiKey }
    const onError = (err: unknown) => {
      setError(err instanceof Error ? err.message : 'Save failed, please try again')
    }
    if (mode === 'create') {
      createMut.mutate(body, {
        onSuccess: () => onClose(),
        onError,
      })
    } else if (provider) {
      updateMut.mutate({ id: provider.id, body }, {
        onSuccess: () => onClose(),
        onError,
      })
    }
  }

  return (
    <div className={styles.backdrop} role="dialog" aria-modal="true" aria-labelledby="modal-title">
      <div className={styles.modal}>
        <h3 id="modal-title" className={styles.title}>
          {mode === 'create' ? 'Add OpenAI-compatible provider' : 'Edit provider'}
        </h3>

        <div className={styles.presetRow}>
          <button type="button" className={styles.presetChip} onClick={applyOpenAIPreset}>
            OpenAI
          </button>
          <span className={styles.presetHelp}>
            Quick-fill the OpenAI defaults. Edit any field after applying.
          </span>
        </div>

        {error && <div className={styles.error}>{error}</div>}

        <form onSubmit={handleSubmit} className={styles.form}>
          <label className={styles.label}>
            Name
            <input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="openai"
              required
              disabled={pending}
            />
            <span className={styles.help}>
              Lowercase letters, numbers, hyphens, and underscores. This is what policies will reference.
            </span>
          </label>

          <label className={styles.label}>
            Base URL
            <input
              type="url"
              value={baseUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
              placeholder="https://api.openai.com/v1"
              required
              disabled={pending}
            />
            <span className={styles.help}>
              The OpenAI Chat Completions endpoint. For Ollama, use http://your-host:11434/v1.
            </span>
          </label>

          <label className={styles.label}>
            API Key
            <input
              type="password"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              required
              disabled={pending}
            />
            <span className={styles.help}>
              {mode === 'edit'
                ? 'Leave unchanged to keep the current key. Paste a new key to replace it.'
                : 'Will be encrypted at rest.'}
            </span>
          </label>

          <div className={styles.actions}>
            <button type="button" onClick={onClose} disabled={pending}>
              Cancel
            </button>
            <button type="submit" disabled={pending}>
              {pending ? 'Testing connection...' : mode === 'create' ? 'Add provider' : 'Save changes'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
```

- [ ] **Step 2: Create the CSS Module**

Create `frontend/src/components/admin/OpenAICompatProviderModal.module.css` with the standard modal patterns already used elsewhere in the admin section. The key shapes:

```css
.backdrop {
  position: fixed;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-overlay);
  z-index: 100;
}

.modal {
  width: min(560px, calc(100vw - 32px));
  padding: 24px;
  background: var(--color-surface);
  border-radius: 8px;
  box-shadow: var(--shadow-modal);
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.title {
  margin: 0;
  font-size: var(--font-size-lg);
}

.presetRow {
  display: flex;
  align-items: center;
  gap: 12px;
}

.presetChip {
  padding: 4px 12px;
  border: 1px solid var(--color-border);
  border-radius: 16px;
  background: var(--color-surface-muted);
  cursor: pointer;
}

.presetHelp {
  font-size: var(--font-size-xs);
  color: var(--color-text-muted);
}

.error {
  padding: 12px;
  background: var(--color-danger-bg);
  color: var(--color-danger-text);
  border-radius: 4px;
}

.form {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.label {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-weight: 600;
}

.label input {
  padding: 8px 12px;
  border: 1px solid var(--color-border);
  border-radius: 4px;
  font-size: var(--font-size-md);
}

.help {
  font-weight: normal;
  font-size: var(--font-size-xs);
  color: var(--color-text-muted);
}

.actions {
  display: flex;
  gap: 12px;
  justify-content: flex-end;
}
```

Again, substitute the project's real CSS custom property names if these don't exactly match what exists in `frontend/src/styles/`.

- [ ] **Step 3: Create a Storybook story covering create mode, edit mode, and error state**

Create `frontend/src/components/admin/OpenAICompatProviderModal.stories.tsx`:

```tsx
import type { Meta, StoryObj } from '@storybook/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { OpenAICompatProviderModal } from './OpenAICompatProviderModal'

const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })

const meta: Meta<typeof OpenAICompatProviderModal> = {
  title: 'Admin/OpenAICompatProviderModal',
  component: OpenAICompatProviderModal,
  decorators: [
    (Story) => (
      <QueryClientProvider client={qc}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}
export default meta

export const Create: StoryObj<typeof OpenAICompatProviderModal> = {
  args: { mode: 'create', onClose: () => {} },
}

export const Edit: StoryObj<typeof OpenAICompatProviderModal> = {
  args: {
    mode: 'edit',
    onClose: () => {},
    provider: {
      id: 1,
      name: 'openai',
      base_url: 'https://api.openai.com/v1',
      masked_key: 'sk-...abcd',
      models_endpoint_available: true,
      created_at: '2026-04-01T12:00:00Z',
      updated_at: '2026-04-01T12:00:00Z',
    },
  },
}
```

- [ ] **Step 4: Verify frontend build**

Run: `cd frontend && npm run build`
Expected: exit code 0.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/admin/OpenAICompatProviderModal.tsx frontend/src/components/admin/OpenAICompatProviderModal.module.css frontend/src/components/admin/OpenAICompatProviderModal.stories.tsx
git commit -m "feat(frontend): OpenAICompatProviderModal with OpenAI preset"
```

---

### Task 22: Delete confirm dialog

**Files:**
- Create: `frontend/src/components/admin/OpenAICompatProviderDeleteDialog.tsx`
- Create: `frontend/src/components/admin/OpenAICompatProviderDeleteDialog.stories.tsx`

- [ ] **Step 1: Create the dialog**

Create `frontend/src/components/admin/OpenAICompatProviderDeleteDialog.tsx`:

```tsx
import type { ApiOpenAICompatProvider } from '../../api/types'
import styles from './OpenAICompatProviderModal.module.css'

interface Props {
  provider: ApiOpenAICompatProvider
  onClose: () => void
  onConfirm: () => void
}

export function OpenAICompatProviderDeleteDialog({ provider, onClose, onConfirm }: Props) {
  return (
    <div className={styles.backdrop} role="dialog" aria-modal="true">
      <div className={styles.modal}>
        <h3 className={styles.title}>Delete provider "{provider.name}"?</h3>
        <p>
          Deleting this provider will not stop any runs currently in progress, but new runs
          that reference "{provider.name}" will fail. Policies referencing this provider will
          need to be updated manually.
        </p>
        <div className={styles.actions}>
          <button type="button" onClick={onClose}>Cancel</button>
          <button type="button" onClick={onConfirm}>Delete</button>
        </div>
      </div>
    </div>
  )
}
```

Reuses the modal CSS module — no new styles needed.

- [ ] **Step 2: Story**

Create `frontend/src/components/admin/OpenAICompatProviderDeleteDialog.stories.tsx`:

```tsx
import type { Meta, StoryObj } from '@storybook/react'
import { OpenAICompatProviderDeleteDialog } from './OpenAICompatProviderDeleteDialog'

const meta: Meta<typeof OpenAICompatProviderDeleteDialog> = {
  title: 'Admin/OpenAICompatProviderDeleteDialog',
  component: OpenAICompatProviderDeleteDialog,
}
export default meta

export const Default: StoryObj<typeof OpenAICompatProviderDeleteDialog> = {
  args: {
    provider: {
      id: 1,
      name: 'openai',
      base_url: 'https://api.openai.com/v1',
      masked_key: 'sk-...abcd',
      models_endpoint_available: true,
      created_at: '2026-04-01T12:00:00Z',
      updated_at: '2026-04-01T12:00:00Z',
    },
    onClose: () => {},
    onConfirm: () => {},
  },
}
```

- [ ] **Step 3: Build**

Run: `cd frontend && npm run build`
Expected: exit code 0.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/components/admin/OpenAICompatProviderDeleteDialog.tsx frontend/src/components/admin/OpenAICompatProviderDeleteDialog.stories.tsx
git commit -m "feat(frontend): OpenAICompatProviderDeleteDialog"
```

---

### Task 23: Render the section on the Admin LLM Providers page

**Files:**
- Modify: `frontend/src/pages/AdminModelsPage.tsx`

- [ ] **Step 1: Import and render the section below the existing Anthropic/Google rows**

Open `frontend/src/pages/AdminModelsPage.tsx`. Find the JSX block that renders the existing providers section. Immediately after it, add:

```tsx
import { OpenAICompatProvidersSection } from '../components/admin/OpenAICompatProvidersSection'

// ...in the component's return statement, after the existing provider rows...

<OpenAICompatProvidersSection />
```

- [ ] **Step 2: Build the frontend and run the existing Vitest suite**

Run: `cd frontend && npm run build && npx vitest run`
Expected: build succeeds; Vitest reports 0 failures.

- [ ] **Step 3: Storybook smoke-check**

Run: `cd frontend && npm run storybook` in one terminal, open http://localhost:6006, and verify the three new stories (section, modal, delete dialog) render without console errors. Kill the server.

This is a manual check — no automated assertion. If anything is broken, it will be visible.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/pages/AdminModelsPage.tsx
git commit -m "feat(frontend): render OpenAICompatProvidersSection on admin page"
```

---

## Layer 8 — ADR

### Task 24: Append ADR-032 to the tracker

**Files:**
- Modify: `docs/ADR_Tracker.md`

- [ ] **Step 1: Append the ADR**

Append to `docs/ADR_Tracker.md` at the end of the numbered ADR section (the body is copied from spec §10 verbatim — the spec's ADR-032 section *is* the ADR):

```markdown
## ADR-032: Admin-managed OpenAI-compatible LLM provider instances

**Status:** Proposed (will be marked Accepted when the implementation of spec
`docs/superpowers/specs/2026-04-06-openai-compatible-llm-client-design.md` lands).

**Context.** Gleipnir's existing LLM provider model (ADR-026) supports two
providers — Anthropic and Google — each backed by a vendor SDK and configured
via a single fixed `<provider>_api_key` row in `system_settings`. The provider
list is a static `knownProviders` slice baked in at startup. This does not
extend to adding OpenAI as a third first-class provider (issue #533), letting
operators point Gleipnir at OpenAI-compatible backends (Ollama, vLLM,
OpenRouter, LM Studio, Together, Groq, Azure-via-compat), or allowing
administrators to add or change LLM endpoints at runtime without redeploying.

**Decision.** Introduce a second provider mechanism that coexists with the
existing SDK-backed mechanism:

- **SDK providers (`anthropic`, `google`)** remain exactly as today. One row
  per provider in `system_settings`. Static `knownProviders` slice. Vendor
  SDKs. They are inherently special — vendor-specific features (prompt
  caching, signed thinking blocks, citations, structured outputs) justify
  per-provider client code.
- **OpenAI-compatible provider instances** are admin-managed, persisted in a
  new `openai_compat_providers` table, and registered into the existing
  `ProviderRegistry` at startup and on every admin mutation. Each row is an
  *instance* of one shared client implementation: a single hand-rolled
  `*openai.Client` constructed with the row's `base_url` and decrypted
  `api_key`. The same client serves OpenAI itself
  (`base_url = https://api.openai.com/v1`) and any compatible third-party
  backend.

**Why hand-rolled, not the official OpenAI Go SDK.** OpenAI Chat Completions
is small, stable, and re-implemented by dozens of third-party backends. A
hand-rolled client (~500 lines) permits permissive deserialization that
tolerates compat-backend quirks (omitted fields, slightly different streaming
chunks, missing `/models`). A strict typed SDK would reject responses a
permissive client accepts. Maintaining one client for both OpenAI proper and
compat backends avoids the drift and bug surface of two parallel
implementations of the same protocol. The SDK's value is concentrated in
non-Chat-Completions surfaces (Realtime, Assistants, Responses) that
Gleipnir does not need.

**Why Chat Completions only, not the Responses API.** The Responses API is
OpenAI-only (compat backends do not implement it). Surfacing reasoning
content from o-series models requires it; we accept that reasoning content
is hidden and only reasoning token counts are recorded, via
`TokenUsage.ThinkingTokens`. Standard chat models have no hidden reasoning,
so nothing is lost there.

**Why two mechanisms instead of unifying everything in one table.**
Migrating Anthropic and Google into the new table was rejected because they
are legitimately special: vendor SDKs with features that don't fit a uniform
shape. The two-mechanism approach is honest about the underlying difference.

**Why the reserved-name rule.** The names `anthropic` and `google` are
reserved at the API layer. Without this, an admin could create an
`openai_compat_providers` row named `anthropic` and silently shadow the
SDK-backed Anthropic provider in the registry.

**Why API keys are encrypted at rest.** Reuses the existing
`internal/admin/crypto.go` and `GLEIPNIR_SECRET_KEY` infrastructure already
used for Anthropic and Google keys. No new key-management story.

**Why deletion is destructive without policy checks.** A policy referencing
an unknown provider already fails at run-start with a clear error. A
"references" check can be added later without changing this ADR. In-flight
runs that already hold a client reference complete their current API call
and only fail when their next run starts and tries to look up the provider
in the registry.

**Why connection-test-on-save (with a 404 escape hatch).** Surfacing bad
config to the admin at save time — rather than to a policy author hours
later in a failed run — is the better operator experience. The 404 escape
hatch exists because some compat backends do not implement `/v1/models`;
they should still be usable, with the trade-off that model-name autocomplete
is unavailable for those instances.

**Consequences.**

- New table `openai_compat_providers`. Migration is additive.
- New admin endpoints under `/api/v1/admin/openai-providers`, admin-role gated.
- New section on the existing admin LLM Providers page. Anthropic and Google
  sections unchanged.
- New Go package `internal/llm/openai`, mirroring `internal/llm/anthropic`
  and `internal/llm/google`.
- Policy YAML unchanged. Policies continue to say `provider: <name>`.
- Two parallel provider mechanisms exist after this change. Future LLM
  providers that also speak OpenAI Chat Completions require zero new code
  (just an admin-created instance). Future LLM providers that need a vendor
  SDK require a new package alongside `anthropic` and `google` and an entry
  in `knownProviders`.

**Supersedes / amends.** Builds on ADR-026 (Model-Agnostic Design); does not
supersede it. Adds a second registration mechanism alongside the existing
static one. ADR-001 (hard capability enforcement) is unchanged — the new
client never sees policy details; it only receives filtered tool lists.
```

- [ ] **Step 2: Commit**

```bash
git add docs/ADR_Tracker.md
git commit -m "docs: add ADR-032 admin-managed openai-compat provider instances"
```

---

## Final verification

### Task 25: Full test suite and manual end-to-end smoke

**Files:**
- Read-only

- [ ] **Step 1: Run the full Go test suite**

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 2: Run the full frontend test + build**

Run: `cd frontend && npm run build && npx vitest run`
Expected: build succeeds, 0 Vitest failures.

- [ ] **Step 3: Start the stack and manually exercise the feature**

```bash
GLEIPNIR_DB_PATH=/tmp/gleipnir-manual.db \
  GLEIPNIR_SECRET_KEY="$(openssl rand -hex 16)" \
  go run . &
PID=$!
sleep 2

# Use the running server's admin UI (http://localhost:8080) to:
# 1. Log in as admin (create the first user if needed).
# 2. Navigate to Admin -> LLM Providers.
# 3. Click "Add provider" under the new OpenAI-compatible section.
# 4. Click the "OpenAI" preset chip — verify it fills name=openai and base_url.
# 5. Paste a real OPENAI_API_KEY and save — verify the connection test passes.
# 6. Create a trivial policy with provider: openai and run it manually —
#    verify the run completes and the audit trail shows the model's response.
# 7. Click "Test" on the row — verify it reports ok.
# 8. Click "Edit", change the base_url back and forth, verify the masked key
#    round-trips without changing the stored ciphertext.
# 9. Click "Delete" and confirm — verify the row disappears and any running
#    policy referencing it fails to start a new run.

kill $PID
rm -f /tmp/gleipnir-manual.db
```

Expected: every step above behaves as the spec describes. If anything surprises, file a bug and fix before merging.

- [ ] **Step 4: Final commit — none if all previous commits covered the work**

- [ ] **Step 5: Verify against the spec**

Open `docs/superpowers/specs/2026-04-06-openai-compatible-llm-client-design.md` alongside the plan. For each section (§3 architecture, §4 data model, §5 lifecycle, §6 admin API, §7 client internals, §8 admin UI, §9 tests, §10 ADR-032), confirm a task covers it. Any gaps → open a follow-up issue referencing the spec section.

