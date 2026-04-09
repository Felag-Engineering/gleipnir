package openai

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	items := buildInput(req, llm.ToolNameMapping{})
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
	items := buildInput(req, llm.ToolNameMapping{})
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

	itemsWith := buildInput(makeReq(withMeta), llm.ToolNameMapping{})
	itemsWithout := buildInput(makeReq(withoutMeta), llm.ToolNameMapping{})

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
