package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rapp992/gleipnir/internal/llm"
	"google.golang.org/genai"
)

// mockGenerator implements contentGenerator for tests. It stores the captured
// arguments from GenerateContent and returns the configured canned response.
type mockGenerator struct {
	response *genai.GenerateContentResponse
	err      error
	captured struct {
		model    string
		contents []*genai.Content
		config   *genai.GenerateContentConfig
	}
}

func (m *mockGenerator) GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	m.captured.model = model
	m.captured.contents = contents
	m.captured.config = config
	return m.response, m.err
}

// makeTextResponse is a helper that builds a minimal genai response with a text part.
func makeTextResponse(text string, finishReason genai.FinishReason, inputTokens, outputTokens int32) *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{{Text: text}},
				},
				FinishReason: finishReason,
			},
		},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     inputTokens,
			CandidatesTokenCount: outputTokens,
		},
	}
}

// --- Request translation tests ---

func TestBuildContents_TextHistory(t *testing.T) {
	history := []llm.ConversationTurn{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hello"}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock{Text: "world"}}},
	}

	contents := buildContents(history)

	if len(contents) != 2 {
		t.Fatalf("expected 2 contents, got %d", len(contents))
	}
	if contents[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", contents[0].Role)
	}
	if len(contents[0].Parts) != 1 || contents[0].Parts[0].Text != "hello" {
		t.Errorf("unexpected user part: %+v", contents[0].Parts)
	}
	if contents[1].Role != "model" {
		t.Errorf("expected role 'model', got %q", contents[1].Role)
	}
	if len(contents[1].Parts) != 1 || contents[1].Parts[0].Text != "world" {
		t.Errorf("unexpected assistant part: %+v", contents[1].Parts)
	}
}

func TestBuildContents_ToolCallBlock(t *testing.T) {
	input := json.RawMessage(`{"key":"value"}`)
	history := []llm.ConversationTurn{
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{
				llm.ToolCallBlock{ID: "call-1", Name: "my_tool", Input: input},
			},
		},
	}

	contents := buildContents(history)

	if len(contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(contents))
	}
	parts := contents[0].Parts
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	fc := parts[0].FunctionCall
	if fc == nil {
		t.Fatal("expected FunctionCall part, got nil")
	}
	if fc.ID != "call-1" {
		t.Errorf("expected ID 'call-1', got %q", fc.ID)
	}
	if fc.Name != "my_tool" {
		t.Errorf("expected Name 'my_tool', got %q", fc.Name)
	}
	if fc.Args["key"] != "value" {
		t.Errorf("expected args key='value', got %v", fc.Args)
	}
}

func TestBuildContents_ToolResultBlock(t *testing.T) {
	history := []llm.ConversationTurn{
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{
				llm.ToolCallBlock{ID: "call-1", Name: "my_tool", Input: json.RawMessage(`{}`)},
			},
		},
		{
			Role: llm.RoleUser,
			Content: []llm.ContentBlock{
				llm.ToolResultBlock{ToolCallID: "call-1", Content: "done", IsError: false},
			},
		},
	}

	contents := buildContents(history)

	if len(contents) != 2 {
		t.Fatalf("expected 2 contents, got %d", len(contents))
	}
	userContent := contents[1]
	if len(userContent.Parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(userContent.Parts))
	}
	fr := userContent.Parts[0].FunctionResponse
	if fr == nil {
		t.Fatal("expected FunctionResponse part, got nil")
	}
	if fr.Name != "my_tool" {
		t.Errorf("expected Name 'my_tool', got %q", fr.Name)
	}
	if fr.ID != "call-1" {
		t.Errorf("expected ID 'call-1', got %q", fr.ID)
	}
	if fr.Response["output"] != "done" {
		t.Errorf("expected output='done', got %v", fr.Response)
	}

	// Also test IsError=true path
	historyErr := []llm.ConversationTurn{
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{
				llm.ToolCallBlock{ID: "call-2", Name: "other_tool", Input: json.RawMessage(`{}`)},
			},
		},
		{
			Role: llm.RoleUser,
			Content: []llm.ContentBlock{
				llm.ToolResultBlock{ToolCallID: "call-2", Content: "failed", IsError: true},
			},
		},
	}
	contentsErr := buildContents(historyErr)
	frErr := contentsErr[1].Parts[0].FunctionResponse
	if frErr.Response["error"] != "failed" {
		t.Errorf("expected error='failed', got %v", frErr.Response)
	}
}

func TestBuildContents_ToolResultBlock_FallbackName(t *testing.T) {
	// ToolResultBlock references a call ID not present in any ToolCallBlock in history.
	history := []llm.ConversationTurn{
		{
			Role: llm.RoleUser,
			Content: []llm.ContentBlock{
				llm.ToolResultBlock{ToolCallID: "orphan-call-id", Content: "result", IsError: false},
			},
		},
	}

	contents := buildContents(history)

	fr := contents[0].Parts[0].FunctionResponse
	if fr.Name != "orphan-call-id" {
		t.Errorf("expected fallback name 'orphan-call-id', got %q", fr.Name)
	}
}

func TestBuildConfig_SystemPrompt(t *testing.T) {
	tests := []struct {
		name         string
		systemPrompt string
		wantNil      bool
		wantText     string
		wantRole     string
	}{
		{
			name:         "non-empty system prompt",
			systemPrompt: "You are a helpful assistant.",
			wantNil:      false,
			wantText:     "You are a helpful assistant.",
			wantRole:     "", // Role must be omitted for SystemInstruction
		},
		{
			name:         "empty system prompt",
			systemPrompt: "",
			wantNil:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := llm.MessageRequest{SystemPrompt: tc.systemPrompt}
			config := buildConfig(req, nil)

			if tc.wantNil {
				if config.SystemInstruction != nil {
					t.Errorf("expected nil SystemInstruction, got %+v", config.SystemInstruction)
				}
				return
			}

			if config.SystemInstruction == nil {
				t.Fatal("expected non-nil SystemInstruction")
			}
			if config.SystemInstruction.Role != tc.wantRole {
				t.Errorf("expected Role=%q, got %q", tc.wantRole, config.SystemInstruction.Role)
			}
			if len(config.SystemInstruction.Parts) != 1 {
				t.Fatalf("expected 1 part, got %d", len(config.SystemInstruction.Parts))
			}
			if config.SystemInstruction.Parts[0].Text != tc.wantText {
				t.Errorf("expected text %q, got %q", tc.wantText, config.SystemInstruction.Parts[0].Text)
			}
		})
	}
}

func TestBuildConfig_MaxTokens(t *testing.T) {
	req := llm.MessageRequest{MaxTokens: 1024}
	config := buildConfig(req, nil)

	if config.MaxOutputTokens != 1024 {
		t.Errorf("expected MaxOutputTokens=1024, got %d", config.MaxOutputTokens)
	}

	// Zero MaxTokens should leave MaxOutputTokens unset.
	reqZero := llm.MessageRequest{MaxTokens: 0}
	configZero := buildConfig(reqZero, nil)
	if configZero.MaxOutputTokens != 0 {
		t.Errorf("expected MaxOutputTokens=0 when MaxTokens is zero, got %d", configZero.MaxOutputTokens)
	}
}

func TestBuildTools(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`)
	tools := []llm.ToolDefinition{
		{Name: "search", Description: "Search the web", InputSchema: schema},
	}

	genaiTools := buildTools(tools)

	if len(genaiTools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(genaiTools))
	}
	decls := genaiTools[0].FunctionDeclarations
	if len(decls) != 1 {
		t.Fatalf("expected 1 function declaration, got %d", len(decls))
	}
	if decls[0].Name != "search" {
		t.Errorf("expected Name='search', got %q", decls[0].Name)
	}
	if decls[0].Description != "Search the web" {
		t.Errorf("expected Description='Search the web', got %q", decls[0].Description)
	}
	if decls[0].Parameters == nil {
		t.Fatal("expected non-nil Parameters")
	}
	if decls[0].Parameters.Type != genai.TypeObject {
		t.Errorf("expected TypeObject, got %v", decls[0].Parameters.Type)
	}
}

// --- Response translation tests ---

func TestTranslateResponse_TextParts(t *testing.T) {
	resp := makeTextResponse("hello world", genai.FinishReasonStop, 10, 5)

	result, err := translateResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Text) != 1 || result.Text[0].Text != "hello world" {
		t.Errorf("unexpected text blocks: %+v", result.Text)
	}
	if result.StopReason != llm.StopReasonEndTurn {
		t.Errorf("expected StopReasonEndTurn, got %v", result.StopReason)
	}
}

func TestTranslateResponse_FunctionCallParts(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{FunctionCall: &genai.FunctionCall{ID: "fc-1", Name: "do_thing", Args: map[string]any{"x": "y"}}},
					},
				},
				FinishReason: genai.FinishReasonStop,
			},
		},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     5,
			CandidatesTokenCount: 10,
		},
	}

	result, err := translateResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
	tc := result.ToolCalls[0]
	if tc.ID != "fc-1" {
		t.Errorf("expected ID='fc-1', got %q", tc.ID)
	}
	if tc.Name != "do_thing" {
		t.Errorf("expected Name='do_thing', got %q", tc.Name)
	}
	if result.StopReason != llm.StopReasonToolUse {
		t.Errorf("expected StopReasonToolUse, got %v", result.StopReason)
	}
}

func TestTranslateResponse_SyntheticID(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{FunctionCall: &genai.FunctionCall{ID: "", Name: "tool", Args: nil}},
					},
				},
				FinishReason: genai.FinishReasonStop,
			},
		},
	}

	result, err := translateResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
	id := result.ToolCalls[0].ID
	if id == "" {
		t.Error("expected non-empty synthetic ID")
	}
	// UUID format: 36 chars with dashes at positions 8, 13, 18, 23.
	if len(id) != 36 {
		t.Errorf("expected 36-char UUID, got %q (len %d)", id, len(id))
	}
	if id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		t.Errorf("expected UUID format with dashes, got %q", id)
	}
}

func TestTranslateResponse_ThoughtPartsAsThinking(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{Text: "reasoning text", Thought: true},
						{Text: "visible text", Thought: false},
					},
				},
				FinishReason: genai.FinishReasonStop,
			},
		},
	}

	result, err := translateResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Thinking) != 1 {
		t.Fatalf("expected 1 thinking block, got %d", len(result.Thinking))
	}
	if result.Thinking[0].Text != "reasoning text" {
		t.Errorf("Thinking[0].Text = %q, want %q", result.Thinking[0].Text, "reasoning text")
	}
	if result.Thinking[0].Redacted {
		t.Errorf("Thinking[0].Redacted = true, want false")
	}
	if len(result.Text) != 1 {
		t.Fatalf("expected 1 text block, got %d", len(result.Text))
	}
	if result.Text[0].Text != "visible text" {
		t.Errorf("expected 'visible text', got %q", result.Text[0].Text)
	}
	if len(result.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(result.ToolCalls))
	}
}

func TestTranslateResponse_ThinkingTokenUsage(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content:      &genai.Content{Parts: []*genai.Part{{Text: "hi"}}},
				FinishReason: genai.FinishReasonStop,
			},
		},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     10,
			CandidatesTokenCount: 20,
			ThoughtsTokenCount:   100,
		},
	}

	result, err := translateResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Usage.ThinkingTokens != 100 {
		t.Errorf("ThinkingTokens = %d, want 100", result.Usage.ThinkingTokens)
	}
	if result.Usage.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", result.Usage.InputTokens)
	}
	if result.Usage.OutputTokens != 20 {
		t.Errorf("OutputTokens = %d, want 20", result.Usage.OutputTokens)
	}
}

func TestCreateMessage_ThinkingBlocks(t *testing.T) {
	mock := &mockGenerator{
		response: &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{
							{Text: "internal reasoning", Thought: true},
							{Text: "visible answer"},
						},
					},
					FinishReason: genai.FinishReasonStop,
				},
			},
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     10,
				CandidatesTokenCount: 5,
				ThoughtsTokenCount:   50,
			},
		},
	}
	client := newClientWithGenerator(mock, nil)

	resp, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model: "gemini-2.0-flash",
		History: []llm.ConversationTurn{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hello"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Thinking) != 1 {
		t.Fatalf("got %d thinking blocks, want 1", len(resp.Thinking))
	}
	if resp.Thinking[0].Text != "internal reasoning" {
		t.Errorf("Thinking[0].Text = %q, want %q", resp.Thinking[0].Text, "internal reasoning")
	}
	if resp.Thinking[0].Redacted {
		t.Errorf("Thinking[0].Redacted = true, want false")
	}
	if resp.Usage.ThinkingTokens != 50 {
		t.Errorf("ThinkingTokens = %d, want 50", resp.Usage.ThinkingTokens)
	}
	if len(resp.Text) != 1 || resp.Text[0].Text != "visible answer" {
		t.Errorf("unexpected text: %+v", resp.Text)
	}
}

func TestTranslateResponse_StopReasons(t *testing.T) {
	toolCallPart := &genai.Part{
		FunctionCall: &genai.FunctionCall{ID: "id-1", Name: "tool"},
	}
	textPart := &genai.Part{Text: "text"}

	tests := []struct {
		name         string
		parts        []*genai.Part
		finishReason genai.FinishReason
		want         llm.StopReason
	}{
		{
			name:         "FinishReasonStop no tool calls → EndTurn",
			parts:        []*genai.Part{textPart},
			finishReason: genai.FinishReasonStop,
			want:         llm.StopReasonEndTurn,
		},
		{
			name:         "FinishReasonStop with tool calls → ToolUse",
			parts:        []*genai.Part{toolCallPart},
			finishReason: genai.FinishReasonStop,
			want:         llm.StopReasonToolUse,
		},
		{
			name:         "FinishReasonMaxTokens → MaxTokens",
			parts:        []*genai.Part{textPart},
			finishReason: genai.FinishReasonMaxTokens,
			want:         llm.StopReasonMaxTokens,
		},
		{
			name:         "FinishReasonSafety → StopReasonError",
			parts:        []*genai.Part{textPart},
			finishReason: genai.FinishReasonSafety,
			want:         llm.StopReasonError,
		},
		{
			name:         "FinishReasonMalformedFunctionCall → StopReasonError",
			parts:        []*genai.Part{textPart},
			finishReason: genai.FinishReasonMalformedFunctionCall,
			want:         llm.StopReasonError,
		},
		{
			name:         "FinishReasonRecitation → StopReasonError",
			parts:        []*genai.Part{textPart},
			finishReason: genai.FinishReasonRecitation,
			want:         llm.StopReasonError,
		},
		{
			name:         "FinishReasonProhibitedContent → StopReasonError",
			parts:        []*genai.Part{textPart},
			finishReason: genai.FinishReasonProhibitedContent,
			want:         llm.StopReasonError,
		},
		{
			name:         "FinishReasonUnspecified → Unknown",
			parts:        []*genai.Part{textPart},
			finishReason: genai.FinishReasonUnspecified,
			want:         llm.StopReasonUnknown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := &genai.GenerateContentResponse{
				Candidates: []*genai.Candidate{
					{
						Content:      &genai.Content{Parts: tc.parts},
						FinishReason: tc.finishReason,
					},
				},
			}
			result, err := translateResponse(resp)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.StopReason != tc.want {
				t.Errorf("expected %v, got %v", tc.want, result.StopReason)
			}
		})
	}
}

func TestTranslateResponse_TokenUsage(t *testing.T) {
	resp := makeTextResponse("hi", genai.FinishReasonStop, 42, 17)

	result, err := translateResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Usage.InputTokens != 42 {
		t.Errorf("expected InputTokens=42, got %d", result.Usage.InputTokens)
	}
	if result.Usage.OutputTokens != 17 {
		t.Errorf("expected OutputTokens=17, got %d", result.Usage.OutputTokens)
	}
}

func TestTranslateResponse_NilCandidates(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: nil,
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     5,
			CandidatesTokenCount: 0,
		},
	}

	result, err := translateResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Text) != 0 {
		t.Errorf("expected no text blocks, got %d", len(result.Text))
	}
	if len(result.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(result.ToolCalls))
	}
	if result.Usage.InputTokens != 5 {
		t.Errorf("expected InputTokens=5, got %d", result.Usage.InputTokens)
	}
}

// --- Error translation tests ---

func TestWrapSDKError_APIError(t *testing.T) {
	tests := []struct {
		name       string
		code       int
		wantSubstr string
	}{
		{name: "rate limit", code: 429, wantSubstr: "rate limited (HTTP 429)"},
		{name: "auth 401", code: 401, wantSubstr: "authentication/permission error (HTTP 401)"},
		{name: "auth 403", code: 403, wantSubstr: "authentication/permission error (HTTP 403)"},
		{name: "server error", code: 500, wantSubstr: "server error (HTTP 500)"},
		{name: "other API error", code: 400, wantSubstr: "API error (HTTP 400)"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			original := genai.APIError{Code: tc.code, Message: "test error"}
			wrapped := wrapSDKError(original)
			if wrapped == nil {
				t.Fatal("expected non-nil error")
			}
			if !strings.Contains(wrapped.Error(), tc.wantSubstr) {
				t.Errorf("expected error to contain %q, got %q", tc.wantSubstr, wrapped.Error())
			}
			// genai.APIError is a value type without an Is() method, so errors.Is
			// won't match. Use errors.As to verify the original error is preserved
			// in the chain.
			var extracted genai.APIError
			if !errors.As(wrapped, &extracted) {
				t.Errorf("expected wrapped error to contain genai.APIError via errors.As")
			}
			if extracted.Code != tc.code {
				t.Errorf("expected extracted Code=%d, got %d", tc.code, extracted.Code)
			}
		})
	}
}

func TestWrapSDKError_NonAPIError(t *testing.T) {
	original := errors.New("connection refused")
	wrapped := wrapSDKError(original)

	if wrapped == nil {
		t.Fatal("expected non-nil error")
	}
	if !strings.HasPrefix(wrapped.Error(), "google: ") {
		t.Errorf("expected error to start with 'google: ', got %q", wrapped.Error())
	}
	if !errors.Is(wrapped, original) {
		t.Errorf("expected wrapped error to wrap original via errors.Is")
	}
}

// --- Integration-level tests via mockGenerator ---

func TestCreateMessage_Success(t *testing.T) {
	mock := &mockGenerator{
		response: makeTextResponse("hello from Gemini", genai.FinishReasonStop, 20, 10),
	}
	client := newClientWithGenerator(mock, nil)

	req := llm.MessageRequest{
		Model:        "gemini-2.0-flash",
		MaxTokens:    512,
		SystemPrompt: "You are helpful.",
		History: []llm.ConversationTurn{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}},
		},
	}

	resp, err := client.CreateMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Text) != 1 || resp.Text[0].Text != "hello from Gemini" {
		t.Errorf("unexpected text: %+v", resp.Text)
	}
	if resp.StopReason != llm.StopReasonEndTurn {
		t.Errorf("expected StopReasonEndTurn, got %v", resp.StopReason)
	}
	if resp.Usage.InputTokens != 20 || resp.Usage.OutputTokens != 10 {
		t.Errorf("unexpected usage: %+v", resp.Usage)
	}
	// Verify captured model and config.
	if mock.captured.model != "gemini-2.0-flash" {
		t.Errorf("expected model 'gemini-2.0-flash', got %q", mock.captured.model)
	}
	if mock.captured.config.MaxOutputTokens != 512 {
		t.Errorf("expected MaxOutputTokens=512, got %d", mock.captured.config.MaxOutputTokens)
	}
	if mock.captured.config.SystemInstruction == nil {
		t.Error("expected non-nil SystemInstruction")
	}
}

func TestCreateMessage_ToolCallResponse(t *testing.T) {
	mock := &mockGenerator{
		response: &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{
							{FunctionCall: &genai.FunctionCall{ID: "tc-1", Name: "search", Args: map[string]any{"q": "go lang"}}},
						},
					},
					FinishReason: genai.FinishReasonStop,
				},
			},
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 15},
		},
	}
	client := newClientWithGenerator(mock, nil)

	resp, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model: "gemini-2.0-flash",
		History: []llm.ConversationTurn{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "search for go"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "search" {
		t.Errorf("expected Name='search', got %q", resp.ToolCalls[0].Name)
	}
	if resp.StopReason != llm.StopReasonToolUse {
		t.Errorf("expected StopReasonToolUse, got %v", resp.StopReason)
	}
}

func TestCreateMessage_SDKError(t *testing.T) {
	apiErr := genai.APIError{Code: 500, Message: "internal error"}
	mock := &mockGenerator{err: apiErr}
	client := newClientWithGenerator(mock, nil)

	_, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model:   "gemini-2.0-flash",
		History: []llm.ConversationTurn{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "server error (HTTP 500)") {
		t.Errorf("expected server error message, got %q", err.Error())
	}
}

func TestCreateMessage_WithHints(t *testing.T) {
	budget := int32(1024)
	groundingEnabled := true
	hints := &GeminiHints{
		ThinkingBudget:  &budget,
		EnableGrounding: &groundingEnabled,
	}
	mock := &mockGenerator{
		response: makeTextResponse("response", genai.FinishReasonStop, 10, 5),
	}
	client := newClientWithGenerator(mock, nil)

	_, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model:   "gemini-2.0-flash",
		Hints:   hints,
		History: []llm.ConversationTurn{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	config := mock.captured.config
	if config.ThinkingConfig == nil {
		t.Fatal("expected non-nil ThinkingConfig")
	}
	if config.ThinkingConfig.ThinkingBudget == nil || *config.ThinkingConfig.ThinkingBudget != 1024 {
		t.Errorf("expected ThinkingBudget=1024, got %v", config.ThinkingConfig.ThinkingBudget)
	}

	// Verify GoogleSearch tool was appended.
	var foundGoogleSearch bool
	for _, tool := range config.Tools {
		if tool.GoogleSearch != nil {
			foundGoogleSearch = true
			break
		}
	}
	if !foundGoogleSearch {
		t.Error("expected GoogleSearch tool to be appended when EnableGrounding=true")
	}
}

// --- StreamMessage tests ---

func TestStreamMessage_SingleChunk(t *testing.T) {
	mock := &mockGenerator{
		response: &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{
					Content: &genai.Content{
						Parts: []*genai.Part{
							{Text: "hello from stream"},
							{FunctionCall: &genai.FunctionCall{ID: "fc-1", Name: "search", Args: map[string]any{"q": "test"}}},
						},
					},
					FinishReason: genai.FinishReasonStop,
				},
			},
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				PromptTokenCount:     25,
				CandidatesTokenCount: 12,
			},
		},
	}
	client := newClientWithGenerator(mock, nil)

	ch, err := client.StreamMessage(context.Background(), llm.MessageRequest{
		Model:   "gemini-2.0-flash",
		History: []llm.ConversationTurn{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "go"}}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	chunk, ok := <-ch
	if !ok {
		t.Fatal("expected chunk from channel, channel was already closed")
	}

	if chunk.Err != nil {
		t.Errorf("expected nil Err, got %v", chunk.Err)
	}
	if chunk.Text == nil {
		t.Fatal("expected non-nil Text")
	}
	if *chunk.Text != "hello from stream" {
		t.Errorf("expected Text=%q, got %q", "hello from stream", *chunk.Text)
	}
	if chunk.ToolCall == nil {
		t.Fatal("expected non-nil ToolCall")
	}
	if chunk.ToolCall.ID != "fc-1" {
		t.Errorf("expected ToolCall.ID=%q, got %q", "fc-1", chunk.ToolCall.ID)
	}
	if chunk.ToolCall.Name != "search" {
		t.Errorf("expected ToolCall.Name=%q, got %q", "search", chunk.ToolCall.Name)
	}
	if chunk.StopReason == nil {
		t.Fatal("expected non-nil StopReason")
	}
	if *chunk.StopReason != llm.StopReasonToolUse {
		t.Errorf("expected StopReasonToolUse, got %v", *chunk.StopReason)
	}
	if chunk.Usage == nil {
		t.Fatal("expected non-nil Usage")
	}
	if chunk.Usage.InputTokens != 25 || chunk.Usage.OutputTokens != 12 {
		t.Errorf("expected Usage={25,12}, got %+v", *chunk.Usage)
	}
}

func TestStreamMessage_ChannelClosed(t *testing.T) {
	mock := &mockGenerator{
		response: makeTextResponse("hi", genai.FinishReasonStop, 5, 3),
	}
	client := newClientWithGenerator(mock, nil)

	ch, err := client.StreamMessage(context.Background(), llm.MessageRequest{
		Model:   "gemini-2.0-flash",
		History: []llm.ConversationTurn{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Consume the single chunk.
	<-ch

	// Channel must be closed — second receive must return ok=false.
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after single chunk")
	}
}

func TestStreamMessage_ErrorPropagation(t *testing.T) {
	mock := &mockGenerator{
		err: genai.APIError{Code: 500, Message: "boom"},
	}
	client := newClientWithGenerator(mock, nil)

	ch, err := client.StreamMessage(context.Background(), llm.MessageRequest{
		Model:   "gemini-2.0-flash",
		History: []llm.ConversationTurn{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "server error") {
		t.Errorf("expected error to contain 'server error', got %q", err.Error())
	}
	if ch != nil {
		t.Error("expected nil channel on error, got non-nil")
	}
}

func TestStreamMessage_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel the context

	// The mock returns context.Canceled to simulate the SDK detecting the
	// cancelled context when GenerateContent is called.
	mock := &mockGenerator{
		err: context.Canceled,
	}
	client := newClientWithGenerator(mock, nil)

	ch, err := client.StreamMessage(ctx, llm.MessageRequest{
		Model:   "gemini-2.0-flash",
		History: []llm.ConversationTurn{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if ch != nil {
		t.Error("expected nil channel on error, got non-nil")
	}
}

// --- ValidateOptions tests ---

func TestValidateOptions(t *testing.T) {
	c := &GeminiClient{}

	tests := []struct {
		name     string
		options  map[string]any
		wantErr  bool
		wantMsgs []string
	}{
		{
			name:    "nil options",
			options: nil,
			wantErr: false,
		},
		{
			name:    "empty options",
			options: map[string]any{},
			wantErr: false,
		},
		{
			name:    "valid thinking_level low",
			options: map[string]any{"thinking_level": "low"},
			wantErr: false,
		},
		{
			name:    "valid thinking_level medium",
			options: map[string]any{"thinking_level": "medium"},
			wantErr: false,
		},
		{
			name:    "valid thinking_level high",
			options: map[string]any{"thinking_level": "high"},
			wantErr: false,
		},
		{
			name:    "valid enable_grounding true",
			options: map[string]any{"enable_grounding": true},
			wantErr: false,
		},
		{
			name:    "valid enable_grounding false",
			options: map[string]any{"enable_grounding": false},
			wantErr: false,
		},
		{
			name:    "valid both options",
			options: map[string]any{"thinking_level": "high", "enable_grounding": true},
			wantErr: false,
		},
		{
			name:     "unknown option",
			options:  map[string]any{"unknown_key": "value"},
			wantErr:  true,
			wantMsgs: []string{"unknown option: unknown_key"},
		},
		{
			name:     "invalid thinking_level value max",
			options:  map[string]any{"thinking_level": "max"},
			wantErr:  true,
			wantMsgs: []string{"must be one of"},
		},
		{
			name:     "invalid thinking_level empty string",
			options:  map[string]any{"thinking_level": ""},
			wantErr:  true,
			wantMsgs: []string{"must be one of"},
		},
		{
			name:     "wrong type for thinking_level",
			options:  map[string]any{"thinking_level": 42},
			wantErr:  true,
			wantMsgs: []string{"expected string, got int"},
		},
		{
			name:     "wrong type for enable_grounding",
			options:  map[string]any{"enable_grounding": "yes"},
			wantErr:  true,
			wantMsgs: []string{"expected bool, got string"},
		},
		{
			name:     "multiple errors collected",
			options:  map[string]any{"bad_key": 1, "thinking_level": "invalid", "enable_grounding": "not-bool"},
			wantErr:  true,
			wantMsgs: []string{"unknown option: bad_key", "must be one of", "expected bool, got string"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := c.ValidateOptions(tc.options)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				for _, msg := range tc.wantMsgs {
					if !strings.Contains(err.Error(), msg) {
						t.Errorf("expected error to contain %q, got %q", msg, err.Error())
					}
				}
			} else {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
			}
		})
	}
}

// --- Model listing and validation tests ---

// mockModelLister implements modelLister for tests.
type mockModelLister struct {
	models []genai.Model
	err    error
	calls  int
}

func (m *mockModelLister) List(_ context.Context, _ *genai.ListModelsConfig) (genai.Page[genai.Model], error) {
	m.calls++
	if m.err != nil {
		return genai.Page[genai.Model]{}, m.err
	}
	items := make([]*genai.Model, len(m.models))
	for i := range m.models {
		items[i] = &m.models[i]
	}
	return genai.Page[genai.Model]{Items: items}, nil
}

func TestGeminiClient_ValidateModelName(t *testing.T) {
	tests := []struct {
		name       string
		models     []genai.Model
		listerErr  error
		modelName  string
		wantErr    bool
		wantSubstr string
	}{
		{
			name: "known model passes",
			models: []genai.Model{
				{Name: "models/gemini-2.0-flash", DisplayName: "Gemini 2.0 Flash"},
				{Name: "models/gemini-1.5-pro", DisplayName: "Gemini 1.5 Pro"},
			},
			modelName: "gemini-2.0-flash",
			wantErr:   false,
		},
		{
			name: "unknown model fails with list",
			models: []genai.Model{
				{Name: "models/gemini-2.0-flash", DisplayName: "Gemini 2.0 Flash"},
			},
			modelName:  "gemini-99",
			wantErr:    true,
			wantSubstr: "unknown Google model",
		},
		{
			name:       "API error surfaces",
			listerErr:  fmt.Errorf("network timeout"),
			modelName:  "gemini-2.0-flash",
			wantErr:    true,
			wantSubstr: "could not validate",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lister := &mockModelLister{models: tc.models, err: tc.listerErr}
			client := newClientWithGenerator(&mockGenerator{}, lister)

			err := client.ValidateModelName(context.Background(), tc.modelName)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
			if tc.wantSubstr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantSubstr)) {
				t.Errorf("error %q does not contain %q", err, tc.wantSubstr)
			}
		})
	}
}

func TestGeminiClient_ListModels(t *testing.T) {
	lister := &mockModelLister{
		models: []genai.Model{
			{Name: "models/gemini-2.0-flash", DisplayName: "Gemini 2.0 Flash"},
			{Name: "models/gemini-1.5-pro", DisplayName: "Gemini 1.5 Pro"},
		},
	}
	client := newClientWithGenerator(&mockGenerator{}, lister)

	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	// Sorted by name.
	if models[0].Name != "gemini-1.5-pro" {
		t.Errorf("models[0].Name = %q, want %q", models[0].Name, "gemini-1.5-pro")
	}
	if models[1].Name != "gemini-2.0-flash" {
		t.Errorf("models[1].Name = %q, want %q", models[1].Name, "gemini-2.0-flash")
	}
	if models[1].DisplayName != "Gemini 2.0 Flash" {
		t.Errorf("models[1].DisplayName = %q, want %q", models[1].DisplayName, "Gemini 2.0 Flash")
	}
}

func TestGeminiClient_InvalidateModelCache(t *testing.T) {
	lister := &mockModelLister{
		models: []genai.Model{
			{Name: "models/gemini-2.0-flash", DisplayName: "Gemini 2.0 Flash"},
		},
	}
	client := newClientWithGenerator(&mockGenerator{}, lister)

	// First call populates cache.
	_, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("first ListModels: %v", err)
	}
	if lister.calls != 1 {
		t.Fatalf("expected 1 API call, got %d", lister.calls)
	}

	// Second call uses cache.
	_, err = client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("second ListModels: %v", err)
	}
	if lister.calls != 1 {
		t.Fatalf("expected still 1 API call (cached), got %d", lister.calls)
	}

	// Invalidate and call again — should re-fetch.
	client.InvalidateModelCache()
	_, err = client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("third ListModels: %v", err)
	}
	if lister.calls != 2 {
		t.Fatalf("expected 2 API calls after invalidation, got %d", lister.calls)
	}
}

func TestGeminiClient_StripModelsPrefix(t *testing.T) {
	lister := &mockModelLister{
		models: []genai.Model{
			{Name: "models/gemini-2.0-flash"},
			{Name: "gemini-no-prefix"},
		},
	}
	client := newClientWithGenerator(&mockGenerator{}, lister)

	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := make(map[string]bool)
	for _, m := range models {
		names[m.Name] = true
	}
	if !names["gemini-2.0-flash"] {
		t.Error("expected 'gemini-2.0-flash' (prefix stripped), not found")
	}
	if !names["gemini-no-prefix"] {
		t.Error("expected 'gemini-no-prefix' (no prefix to strip), not found")
	}
}
