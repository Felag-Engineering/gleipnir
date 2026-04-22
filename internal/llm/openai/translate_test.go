package openai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openai/openai-go/responses"
	"github.com/rapp992/gleipnir/internal/llm"
)

func TestTranslateResponse_TextOnly(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "response_text_only.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var resp responses.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	out, err := translateResponse(&resp, llm.ToolNameMapping{})
	if err != nil {
		t.Fatalf("translateResponse: %v", err)
	}
	if len(out.Text) == 0 {
		t.Fatal("expected text block")
	}
	if out.Text[0].Text != "Hello from OpenAI." {
		t.Errorf("text = %q", out.Text[0].Text)
	}
	if out.StopReason != llm.StopReasonEndTurn {
		t.Errorf("StopReason = %v; want EndTurn", out.StopReason)
	}
	if out.Usage.InputTokens != 5 {
		t.Errorf("InputTokens = %d; want 5", out.Usage.InputTokens)
	}
}

func TestTranslateResponse_ToolCalls(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "response_with_tool_calls.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var resp responses.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	out, err := translateResponse(&resp, llm.ToolNameMapping{})
	if err != nil {
		t.Fatalf("translateResponse: %v", err)
	}
	if len(out.ToolCalls) == 0 {
		t.Fatal("expected tool call")
	}
	tc := out.ToolCalls[0]
	if tc.Name != "get_weather" {
		t.Errorf("tool name = %q", tc.Name)
	}
	if tc.ID != "call_abc123" {
		t.Errorf("call_id = %q", tc.ID)
	}
	if out.StopReason != llm.StopReasonToolUse {
		t.Errorf("StopReason = %v; want ToolUse", out.StopReason)
	}
}

func TestTranslateResponse_ToolNameReversal(t *testing.T) {
	// Verify that sanitized→original name reversal works.
	raw, err := os.ReadFile(filepath.Join("testdata", "response_with_tool_calls.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var resp responses.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// The fixture uses "get_weather"; simulate that it was sanitized from
	// "tools.get_weather" → "tools-get_weather" and the fixture has "get_weather".
	// Use a simple map where "get_weather" → "tools.get_weather".
	names := llm.ToolNameMapping{
		SanitizedToOriginal: map[string]string{"get_weather": "tools.get_weather"},
		OriginalToSanitized: map[string]string{"tools.get_weather": "get_weather"},
	}
	out, err := translateResponse(&resp, names)
	if err != nil {
		t.Fatalf("translateResponse: %v", err)
	}
	if len(out.ToolCalls) == 0 {
		t.Fatal("expected tool call")
	}
	if out.ToolCalls[0].Name != "tools.get_weather" {
		t.Errorf("name = %q; want tools.get_weather", out.ToolCalls[0].Name)
	}
}

func TestTranslateResponse_ReasoningTokensAndBlock(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "response_with_reasoning.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var resp responses.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	out, err := translateResponse(&resp, llm.ToolNameMapping{})
	if err != nil {
		t.Fatalf("translateResponse: %v", err)
	}
	if out.Usage.ThinkingTokens != 15 {
		t.Errorf("ThinkingTokens = %d; want 15", out.Usage.ThinkingTokens)
	}
	if len(out.Thinking) == 0 {
		t.Fatal("expected thinking block")
	}
}

func TestBuildTools_NameSanitization(t *testing.T) {
	// The Responses API allows [a-zA-Z0-9_-]. SanitizeToolName with "-" as
	// allowedExtra turns dots into underscores (only hyphens are preserved).
	tools := []llm.ToolDefinition{
		{Name: "tools.get_data", Description: "gets data", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	result, names, err := buildTools(tools)
	if err != nil {
		t.Fatalf("buildTools: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d; want 1", len(result))
	}
	// "tools.get_data" sanitizes to "tools_get_data" (dot → underscore)
	if result[0].OfFunction.Name != "tools_get_data" {
		t.Errorf("sanitized name = %q; want tools_get_data", result[0].OfFunction.Name)
	}
	if names.SanitizedToOriginal["tools_get_data"] != "tools.get_data" {
		t.Errorf("reverse map wrong: %v", names.SanitizedToOriginal)
	}
}

func TestBuildTools_Collision(t *testing.T) {
	// "tools.foo" → "tools_foo" and "tools_foo" → "tools_foo" collide.
	tools := []llm.ToolDefinition{
		{Name: "tools.foo"},
		{Name: "tools_foo"}, // same sanitized form
	}
	_, _, err := buildTools(tools)
	if err == nil {
		t.Error("expected collision error, got nil")
	}
}

func TestBuildInput_UserTurn(t *testing.T) {
	req := llm.MessageRequest{
		Model: "gpt-4o",
		History: []llm.ConversationTurn{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "Hello"}}},
		},
	}
	items, err := buildInput(req, llm.ToolNameMapping{})
	if err != nil {
		t.Fatalf("buildInput: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d; want 1", len(items))
	}
}

func TestBuildInput_ToolResultTurn(t *testing.T) {
	req := llm.MessageRequest{
		Model: "gpt-4o",
		History: []llm.ConversationTurn{
			{
				Role: llm.RoleUser,
				Content: []llm.ContentBlock{
					llm.ToolResultBlock{ToolCallID: "call_xyz", Content: "sunny", IsError: false},
				},
			},
		},
	}
	items, err := buildInput(req, llm.ToolNameMapping{})
	if err != nil {
		t.Fatalf("buildInput: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d; want 1", len(items))
	}
}

func TestTranslate_IgnoresProviderMetadata(t *testing.T) {
	// A ToolCallBlock carrying Google-specific metadata must produce the same
	// OpenAI Responses API input item as one without. The translator reads
	// only ID, Name, and Input.
	input := json.RawMessage(`{"city":"SF"}`)

	withMeta := llm.ToolCallBlock{
		ID:    "call_1",
		Name:  "get_weather",
		Input: input,
		ProviderMetadata: map[string][]byte{
			"google.thought_signature": {0xca, 0xfe},
		},
	}
	withoutMeta := llm.ToolCallBlock{
		ID:    "call_1",
		Name:  "get_weather",
		Input: input,
	}

	makeReq := func(block llm.ToolCallBlock) llm.MessageRequest {
		return llm.MessageRequest{
			Model: "gpt-4o",
			History: []llm.ConversationTurn{
				{Role: llm.RoleAssistant, Content: []llm.ContentBlock{block}},
			},
		}
	}

	itemsWith, err := buildInput(makeReq(withMeta), llm.ToolNameMapping{})
	if err != nil {
		t.Fatalf("buildInput (with): %v", err)
	}
	itemsWithout, err := buildInput(makeReq(withoutMeta), llm.ToolNameMapping{})
	if err != nil {
		t.Fatalf("buildInput (without): %v", err)
	}

	if len(itemsWith) != 1 || len(itemsWithout) != 1 {
		t.Fatalf("expected 1 item each, got %d and %d", len(itemsWith), len(itemsWithout))
	}

	// Both should produce a FunctionCall item; marshal both and compare.
	rawWith, err := json.Marshal(itemsWith[0])
	if err != nil {
		t.Fatalf("marshal with meta: %v", err)
	}
	rawWithout, err := json.Marshal(itemsWithout[0])
	if err != nil {
		t.Fatalf("marshal without meta: %v", err)
	}
	if string(rawWith) != string(rawWithout) {
		t.Errorf("wire payload differs:\nwith    metadata: %s\nwithout metadata: %s", rawWith, rawWithout)
	}
}

func TestTranslateResponse_ReasoningWithEncryptedContent(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "response_with_reasoning.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var resp responses.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	out, err := translateResponse(&resp, llm.ToolNameMapping{})
	if err != nil {
		t.Fatalf("translateResponse: %v", err)
	}
	if len(out.Thinking) == 0 {
		t.Fatal("expected at least one thinking block")
	}
	tb := out.Thinking[0]
	var state openaiThinkingState
	if err := json.Unmarshal(tb.ProviderState, &state); err != nil {
		t.Fatalf("unmarshal ProviderState: %v", err)
	}
	if state.EncryptedContent != "enc_abc123" {
		t.Errorf("state.EncryptedContent = %q, want enc_abc123", state.EncryptedContent)
	}
	if state.ID != "rs_001" {
		t.Errorf("state.ID = %q, want rs_001", state.ID)
	}
}

func TestTranslateResponse_ReasoningNoEncryptedContent_WithSummary(t *testing.T) {
	// Build a response with a reasoning item that has summary but no encrypted_content.
	fixture := `{
		"id": "resp_x",
		"object": "response",
		"created_at": 1700000000,
		"model": "o3-mini",
		"status": "completed",
		"output": [
			{
				"id": "rs_002",
				"type": "reasoning",
				"summary": [{"type": "summary_text", "text": "thinking hard"}],
				"status": "completed"
			}
		],
		"usage": {"input_tokens": 5, "output_tokens": 10, "total_tokens": 15,
			"input_tokens_details": {"cached_tokens": 0},
			"output_tokens_details": {"reasoning_tokens": 5}}
	}`
	var resp responses.Response
	if err := json.Unmarshal([]byte(fixture), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	out, err := translateResponse(&resp, llm.ToolNameMapping{})
	if err != nil {
		t.Fatalf("translateResponse: %v", err)
	}
	if len(out.Thinking) == 0 {
		t.Fatal("expected thinking block when summary text is present")
	}
	tb := out.Thinking[0]
	if tb.Text != "thinking hard" {
		t.Errorf("Text = %q, want thinking hard", tb.Text)
	}
	var state openaiThinkingState
	if err := json.Unmarshal(tb.ProviderState, &state); err != nil {
		t.Fatalf("unmarshal ProviderState: %v", err)
	}
	if state.EncryptedContent != "" {
		t.Errorf("state.EncryptedContent should be empty, got %q", state.EncryptedContent)
	}
}

func TestTranslateResponse_ReasoningEmpty_Skipped(t *testing.T) {
	// A reasoning item with no summary and no encrypted_content should be dropped.
	fixture := `{
		"id": "resp_x",
		"object": "response",
		"created_at": 1700000000,
		"model": "o3-mini",
		"status": "completed",
		"output": [
			{
				"id": "rs_003",
				"type": "reasoning",
				"summary": [],
				"status": "completed"
			}
		],
		"usage": {"input_tokens": 5, "output_tokens": 5, "total_tokens": 10,
			"input_tokens_details": {"cached_tokens": 0},
			"output_tokens_details": {"reasoning_tokens": 0}}
	}`
	var resp responses.Response
	if err := json.Unmarshal([]byte(fixture), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	out, err := translateResponse(&resp, llm.ToolNameMapping{})
	if err != nil {
		t.Fatalf("translateResponse: %v", err)
	}
	if len(out.Thinking) != 0 {
		t.Errorf("expected no thinking blocks, got %d", len(out.Thinking))
	}
}

func TestBuildInput_ThinkingBlockRoundTrip(t *testing.T) {
	providerState, _ := marshalThinkingState(openaiThinkingState{ID: "rs_001", EncryptedContent: "enc123"})
	req := llm.MessageRequest{
		Model: "o3-mini",
		History: []llm.ConversationTurn{
			{
				Role: llm.RoleAssistant,
				Content: []llm.ContentBlock{
					llm.ThinkingBlock{Provider: "openai", Text: "summary text", ProviderState: providerState},
					llm.TextBlock{Text: "the answer"},
				},
			},
		},
	}
	items, err := buildInput(req, llm.ToolNameMapping{})
	if err != nil {
		t.Fatalf("buildInput: %v", err)
	}
	// Expect two items: reasoning item + assistant text item.
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}

	raw, err := json.Marshal(items[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, `"id":"rs_001"`) {
		t.Errorf("expected id=rs_001 in %s", s)
	}
	if !strings.Contains(s, `"enc123"`) {
		t.Errorf("expected encrypted_content in %s", s)
	}
	if !strings.Contains(s, `"summary text"`) {
		t.Errorf("expected summary text in %s", s)
	}
}

func TestBuildInput_ThinkingBlockEmptySummary_IncludesSummaryField(t *testing.T) {
	// A ThinkingBlock with encrypted_content but no summary text must still
	// produce a reasoning item with a non-nil summary array. The Responses API
	// rejects input items where the summary field is absent (nil slice → omitted
	// via omitzero).
	providerState, _ := marshalThinkingState(openaiThinkingState{ID: "rs_010", EncryptedContent: "enc_opaque"})
	req := llm.MessageRequest{
		Model: "o3-mini",
		History: []llm.ConversationTurn{
			{
				Role: llm.RoleAssistant,
				Content: []llm.ContentBlock{
					llm.ThinkingBlock{Provider: "openai", Text: "", ProviderState: providerState},
				},
			},
		},
	}
	items, err := buildInput(req, llm.ToolNameMapping{})
	if err != nil {
		t.Fatalf("buildInput: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	raw, err := json.Marshal(items[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, `"summary"`) {
		t.Errorf("expected summary field in %s", s)
	}
	if !strings.Contains(s, `"enc_opaque"`) {
		t.Errorf("expected encrypted_content in %s", s)
	}
}

func TestBuildInput_ThinkingBlockNoEncryptedContent(t *testing.T) {
	// A ThinkingBlock with only summary text (no EncryptedContent) should still
	// emit a reasoning input item.
	providerState, _ := marshalThinkingState(openaiThinkingState{ID: "rs_002"})
	req := llm.MessageRequest{
		Model: "o3-mini",
		History: []llm.ConversationTurn{
			{
				Role: llm.RoleAssistant,
				Content: []llm.ContentBlock{
					llm.ThinkingBlock{Provider: "openai", Text: "some reasoning", ProviderState: providerState},
				},
			},
		},
	}
	items, err := buildInput(req, llm.ToolNameMapping{})
	if err != nil {
		t.Fatalf("buildInput: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	raw, err := json.Marshal(items[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, `"rs_002"`) {
		t.Errorf("expected id rs_002 in %s", s)
	}
}

func TestBuildInput_ThinkingBlockNoIDNoEncrypted_Skipped(t *testing.T) {
	// A ThinkingBlock from a different provider (Anthropic) must be silently
	// dropped — the OpenAI translator cannot round-trip Anthropic state.
	providerState := json.RawMessage(`{"signature":"sig_xyz"}`)
	req := llm.MessageRequest{
		Model: "o3-mini",
		History: []llm.ConversationTurn{
			{
				Role: llm.RoleAssistant,
				Content: []llm.ContentBlock{
					llm.ThinkingBlock{Provider: "anthropic", Text: "anthropic reasoning", ProviderState: providerState},
					llm.TextBlock{Text: "answer"},
				},
			},
		},
	}
	items, err := buildInput(req, llm.ToolNameMapping{})
	if err != nil {
		t.Fatalf("buildInput: %v", err)
	}
	// Only the text block should produce an item; ThinkingBlock is skipped.
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1 (cross-provider ThinkingBlock should be skipped)", len(items))
	}
	raw, err := json.Marshal(items[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), "answer") {
		t.Errorf("expected text 'answer' in item, got %s", raw)
	}
}

func TestBuildInput_ThinkingBlockMalformedProviderState_Error(t *testing.T) {
	// A ThinkingBlock with malformed ProviderState JSON must return an error.
	req := llm.MessageRequest{
		Model: "o3-mini",
		History: []llm.ConversationTurn{
			{
				Role: llm.RoleAssistant,
				Content: []llm.ContentBlock{
					llm.ThinkingBlock{
						Provider:      "openai",
						Text:          "reasoning",
						ProviderState: json.RawMessage([]byte("{not json")),
					},
				},
			},
		},
	}
	_, err := buildInput(req, llm.ToolNameMapping{})
	if err == nil {
		t.Fatal("expected error for malformed ProviderState, got nil")
	}
	if !strings.Contains(err.Error(), "unmarshal thinking state") {
		t.Errorf("error message %q does not contain 'unmarshal thinking state'", err.Error())
	}
}
