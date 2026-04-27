package google

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/llm"
	"google.golang.org/genai"
)

// mockGenerator implements contentGenerator for tests. It stores the captured
// arguments from GenerateContent and returns the configured canned response.
// streamResponses and streamErr drive the GenerateContentStream path.
type mockGenerator struct {
	response        *genai.GenerateContentResponse
	err             error
	streamResponses []*genai.GenerateContentResponse
	streamErr       error
	captured        struct {
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

// GenerateContentStream returns an iter.Seq2 that yields each response in
// streamResponses, then yields (nil, streamErr) if set.
func (m *mockGenerator) GenerateContentStream(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error] {
	return func(yield func(*genai.GenerateContentResponse, error) bool) {
		for _, resp := range m.streamResponses {
			if !yield(resp, nil) {
				return
			}
		}
		if m.streamErr != nil {
			yield(nil, m.streamErr)
		}
	}
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

	contents := buildContents(history, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})

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

	contents := buildContents(history, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})

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

	contents := buildContents(history, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})

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
	contentsErr := buildContents(historyErr, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})
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

	contents := buildContents(history, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})

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
			config := buildConfig(req, nil, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})

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
	config := buildConfig(req, nil, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})

	if config.MaxOutputTokens != 1024 {
		t.Errorf("expected MaxOutputTokens=1024, got %d", config.MaxOutputTokens)
	}

	// Zero MaxTokens should leave MaxOutputTokens unset.
	reqZero := llm.MessageRequest{MaxTokens: 0}
	configZero := buildConfig(reqZero, nil, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})
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

	result, err := translateResponse(resp, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})
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

	result, err := translateResponse(resp, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})
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

	result, err := translateResponse(resp, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})
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

	result, err := translateResponse(resp, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})
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

func TestTranslateResponse_EmptyThoughtPartSkipped(t *testing.T) {
	// Gemini occasionally emits thought parts with an empty text field. These
	// should be silently ignored rather than producing empty ThinkingBlocks.
	tests := []struct {
		name             string
		parts            []*genai.Part
		wantThinking     int
		wantThinkingText string
	}{
		{
			name:         "empty thought produces no ThinkingBlock",
			parts:        []*genai.Part{{Text: "", Thought: true}},
			wantThinking: 0,
		},
		{
			name:             "non-empty thought produces a ThinkingBlock",
			parts:            []*genai.Part{{Text: "some reasoning", Thought: true}},
			wantThinking:     1,
			wantThinkingText: "some reasoning",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := &genai.GenerateContentResponse{
				Candidates: []*genai.Candidate{
					{
						Content:      &genai.Content{Parts: tc.parts},
						FinishReason: genai.FinishReasonStop,
					},
				},
			}
			result, err := translateResponse(resp, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(result.Thinking) != tc.wantThinking {
				t.Fatalf("len(Thinking) = %d, want %d", len(result.Thinking), tc.wantThinking)
			}
			if tc.wantThinking > 0 && result.Thinking[0].Text != tc.wantThinkingText {
				t.Errorf("Thinking[0].Text = %q, want %q", result.Thinking[0].Text, tc.wantThinkingText)
			}
		})
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

	result, err := translateResponse(resp, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})
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
	client := newClientWithGenerator(mock)

	resp, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model: "gemini-2.5-flash",
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

// --- ThoughtSignature round-trip tests ---

func TestTranslateResponse_CapturesThoughtSignature(t *testing.T) {
	sig := []byte{0xde, 0xad, 0xbe, 0xef}
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{
							FunctionCall:     &genai.FunctionCall{ID: "fc-1", Name: "do_thing", Args: nil},
							ThoughtSignature: sig,
						},
					},
				},
				FinishReason: genai.FinishReasonStop,
			},
		},
	}

	result, err := translateResponse(resp, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
	got := result.ToolCalls[0].ProviderMetadata["google.thought_signature"]
	if string(got) != string(sig) {
		t.Errorf("thought_signature = %v, want %v", got, sig)
	}
}

func TestTranslateResponse_NoThoughtSignature_NilMetadata(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{FunctionCall: &genai.FunctionCall{ID: "fc-1", Name: "do_thing", Args: nil}},
					},
				},
				FinishReason: genai.FinishReasonStop,
			},
		},
	}

	result, err := translateResponse(resp, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].ProviderMetadata != nil {
		t.Errorf("expected nil ProviderMetadata when no ThoughtSignature, got %v", result.ToolCalls[0].ProviderMetadata)
	}
}

func TestBuildContents_ToolCallBlock_WithThoughtSignature(t *testing.T) {
	sig := []byte{0x01, 0x02, 0x03}
	history := []llm.ConversationTurn{
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{
				llm.ToolCallBlock{
					ID:    "call-1",
					Name:  "my_tool",
					Input: json.RawMessage(`{"key":"value"}`),
					ProviderMetadata: map[string][]byte{
						"google.thought_signature": sig,
					},
				},
			},
		},
	}

	contents := buildContents(history, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})

	if len(contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(contents))
	}
	parts := contents[0].Parts
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	part := parts[0]
	if part.FunctionCall == nil {
		t.Fatal("expected FunctionCall part, got nil")
	}
	if part.FunctionCall.ID != "call-1" {
		t.Errorf("expected ID 'call-1', got %q", part.FunctionCall.ID)
	}
	if part.FunctionCall.Name != "my_tool" {
		t.Errorf("expected Name 'my_tool', got %q", part.FunctionCall.Name)
	}
	if string(part.ThoughtSignature) != string(sig) {
		t.Errorf("ThoughtSignature = %v, want %v", part.ThoughtSignature, sig)
	}
}

func TestBuildContents_ToolCallBlock_NilMetadata_NoSignature(t *testing.T) {
	history := []llm.ConversationTurn{
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{
				llm.ToolCallBlock{ID: "call-1", Name: "my_tool", Input: json.RawMessage(`{}`)},
			},
		},
	}

	contents := buildContents(history, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})

	if len(contents[0].Parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(contents[0].Parts))
	}
	part := contents[0].Parts[0]
	if len(part.ThoughtSignature) != 0 {
		t.Errorf("expected empty ThoughtSignature, got %v", part.ThoughtSignature)
	}
}

func TestGoogle_ThoughtSignature_InMemoryRoundTrip(t *testing.T) {
	sig := []byte{0xca, 0xfe, 0xba, 0xbe}

	// Step a: build a fake response carrying a FunctionCall with a ThoughtSignature.
	fakeResp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{
							FunctionCall:     &genai.FunctionCall{ID: "fc-99", Name: "run_task", Args: map[string]any{"x": "1"}},
							ThoughtSignature: sig,
						},
					},
				},
				FinishReason: genai.FinishReasonStop,
			},
		},
	}

	names := llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}}

	msgResp, err := translateResponse(fakeResp, names)
	if err != nil {
		t.Fatalf("translateResponse: %v", err)
	}
	if len(msgResp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msgResp.ToolCalls))
	}

	// Step b: construct the history turn as the agent loop would.
	historyTurn := llm.ConversationTurn{
		Role:    llm.RoleAssistant,
		Content: []llm.ContentBlock{msgResp.ToolCalls[0]},
	}

	// Step c: call buildContents with that history.
	contents := buildContents([]llm.ConversationTurn{historyTurn}, names)

	if len(contents) != 1 || len(contents[0].Parts) != 1 {
		t.Fatalf("expected 1 content with 1 part, got contents=%d", len(contents))
	}

	// Step d: assert the outgoing Part carries the original signature bytes.
	outPart := contents[0].Parts[0]
	if string(outPart.ThoughtSignature) != string(sig) {
		t.Errorf("round-tripped ThoughtSignature = %v, want %v", outPart.ThoughtSignature, sig)
	}
	if outPart.FunctionCall == nil || outPart.FunctionCall.ID != "fc-99" {
		t.Errorf("FunctionCall fields altered: %+v", outPart.FunctionCall)
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
			result, err := translateResponse(resp, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})
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

	result, err := translateResponse(resp, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})
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

	result, err := translateResponse(resp, llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}})
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
	client := newClientWithGenerator(mock)

	req := llm.MessageRequest{
		Model:        "gemini-2.5-flash",
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
	if mock.captured.model != "gemini-2.5-flash" {
		t.Errorf("expected model 'gemini-2.5-flash', got %q", mock.captured.model)
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
	client := newClientWithGenerator(mock)

	resp, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model: "gemini-2.5-flash",
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
	client := newClientWithGenerator(mock)

	_, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model:   "gemini-2.5-flash",
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
	client := newClientWithGenerator(mock)

	_, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model:   "gemini-2.5-flash",
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

func TestStreamMessage_HappyPath(t *testing.T) {
	// Two responses: text in the first, tool call in the second with usage.
	mock := &mockGenerator{
		streamResponses: []*genai.GenerateContentResponse{
			{
				Candidates: []*genai.Candidate{
					{
						Content: &genai.Content{
							Parts: []*genai.Part{{Text: "hello from stream"}},
						},
					},
				},
			},
			{
				Candidates: []*genai.Candidate{
					{
						Content: &genai.Content{
							Parts: []*genai.Part{
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
		},
	}
	client := newClientWithGenerator(mock)

	ch, err := client.StreamMessage(context.Background(), llm.MessageRequest{
		Model:   "gemini-2.5-flash",
		History: []llm.ConversationTurn{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "go"}}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var chunks []llm.MessageChunk
	for c := range ch {
		chunks = append(chunks, c)
	}

	// Expect: 1 text chunk + 1 tool call chunk + 1 final chunk.
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}

	textChunk := chunks[0]
	if textChunk.Err != nil {
		t.Errorf("expected nil Err, got %v", textChunk.Err)
	}
	if textChunk.Text == nil || *textChunk.Text != "hello from stream" {
		t.Errorf("chunks[0].Text = %v, want %q", textChunk.Text, "hello from stream")
	}
	if textChunk.StopReason != nil {
		t.Error("chunks[0].StopReason should be nil (not the final chunk)")
	}

	toolChunk := chunks[1]
	if toolChunk.ToolCall == nil {
		t.Fatal("chunks[1].ToolCall is nil, want non-nil")
	}
	if toolChunk.ToolCall.ID != "fc-1" {
		t.Errorf("chunks[1].ToolCall.ID = %q, want %q", toolChunk.ToolCall.ID, "fc-1")
	}
	if toolChunk.ToolCall.Name != "search" {
		t.Errorf("chunks[1].ToolCall.Name = %q, want %q", toolChunk.ToolCall.Name, "search")
	}

	final := chunks[2]
	if final.StopReason == nil || *final.StopReason != llm.StopReasonToolUse {
		t.Errorf("final.StopReason = %v, want StopReasonToolUse", final.StopReason)
	}
	if final.Usage == nil || final.Usage.InputTokens != 25 || final.Usage.OutputTokens != 12 {
		t.Errorf("final.Usage = %v, want {InputTokens:25, OutputTokens:12}", final.Usage)
	}
}

func TestStreamMessage_ChannelClosed(t *testing.T) {
	mock := &mockGenerator{
		streamResponses: []*genai.GenerateContentResponse{
			makeTextResponse("hi", genai.FinishReasonStop, 5, 3),
		},
	}
	client := newClientWithGenerator(mock)

	ch, err := client.StreamMessage(context.Background(), llm.MessageRequest{
		Model:   "gemini-2.5-flash",
		History: []llm.ConversationTurn{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Drain all chunks before checking closure.
	for range ch {
	}

	// Channel must be closed — subsequent receive must return ok=false.
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after all chunks consumed")
	}
}

func TestStreamMessage_StreamError(t *testing.T) {
	// Errors now arrive as Err chunks (not synchronous returns) because
	// GenerateContentStream is lazy — the iterator yields errors asynchronously.
	mock := &mockGenerator{
		streamErr: errors.New("server error: boom"),
	}
	client := newClientWithGenerator(mock)

	ch, err := client.StreamMessage(context.Background(), llm.MessageRequest{
		Model:   "gemini-2.5-flash",
		History: []llm.ConversationTurn{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("unexpected synchronous error: %v", err)
	}

	var errChunk *llm.MessageChunk
	for c := range ch {
		if c.Err != nil {
			cp := c
			errChunk = &cp
			break
		}
	}
	for range ch { // drain remainder
	}

	if errChunk == nil {
		t.Fatal("expected an Err chunk from stream error, got none")
	}
	if !strings.Contains(errChunk.Err.Error(), "server error") {
		t.Errorf("Err chunk error %q does not contain %q", errChunk.Err.Error(), "server error")
	}
}

func TestStreamMessage_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel the context

	// The mock has no responses; with a pre-cancelled context the ctx.Done()
	// check in consumeStream fires on the first response (or immediately).
	mock := &mockGenerator{
		streamResponses: []*genai.GenerateContentResponse{
			makeTextResponse("hi", genai.FinishReasonStop, 1, 1),
		},
	}
	client := newClientWithGenerator(mock)

	ch, err := client.StreamMessage(ctx, llm.MessageRequest{
		Model:   "gemini-2.5-flash",
		History: []llm.ConversationTurn{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}}},
	})
	// StreamMessage itself returns (ch, nil) — the cancellation surfaces as an Err chunk.
	if err != nil {
		t.Fatalf("unexpected synchronous error: %v", err)
	}
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}

	// Drain; there should be an Err chunk carrying ctx.Err().
	var errChunk *llm.MessageChunk
	for c := range ch {
		if c.Err != nil {
			cp := c
			errChunk = &cp
			break
		}
	}
	for range ch {
	}

	if errChunk == nil {
		t.Fatal("expected an Err chunk from cancelled context, got none")
	}
	if !errors.Is(errChunk.Err, context.Canceled) {
		t.Errorf("Err = %v, want context.Canceled", errChunk.Err)
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
			name:    "valid thinking_level minimal",
			options: map[string]any{"thinking_level": "minimal"},
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
			name:    "valid thinking_budget",
			options: map[string]any{"thinking_budget": 1024},
			wantErr: false,
		},
		{
			name:     "thinking_budget zero",
			options:  map[string]any{"thinking_budget": 0},
			wantErr:  true,
			wantMsgs: []string{"must be > 0"},
		},
		{
			name:     "thinking_budget negative",
			options:  map[string]any{"thinking_budget": -1},
			wantErr:  true,
			wantMsgs: []string{"must be > 0"},
		},
		{
			name:     "thinking_budget non-integer",
			options:  map[string]any{"thinking_budget": 1.5},
			wantErr:  true,
			wantMsgs: []string{"must be an integer"},
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
			name:    "valid all three options",
			options: map[string]any{"thinking_level": "high", "thinking_budget": 512, "enable_grounding": true},
			wantErr: false,
		},
		{
			name:     "unknown option",
			options:  map[string]any{"unknown_key": "value"},
			wantErr:  true,
			wantMsgs: []string{"unknown option"},
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
			wantMsgs: []string{"must be a string"},
		},
		{
			name:     "wrong type for enable_grounding",
			options:  map[string]any{"enable_grounding": "yes"},
			wantErr:  true,
			wantMsgs: []string{"must be a bool"},
		},
		{
			// parseHints returns on first error; we only verify an error is returned
			// since map iteration order is non-deterministic.
			name:    "multiple invalid options",
			options: map[string]any{"bad_key": 1, "thinking_level": "invalid", "enable_grounding": "not-bool"},
			wantErr: true,
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

func TestBuildConfig_ThinkingLevel(t *testing.T) {
	noMapping := llm.ToolNameMapping{SanitizedToOriginal: map[string]string{}, OriginalToSanitized: map[string]string{}}

	tests := []struct {
		name              string
		hints             *GeminiHints
		wantThinkingLevel genai.ThinkingLevel
		wantBudget        *int32
		wantConfigNil     bool
	}{
		{
			name:          "nil hints — ThinkingConfig not set",
			hints:         nil,
			wantConfigNil: true,
		},
		{
			name:          "empty hints — ThinkingConfig not set",
			hints:         &GeminiHints{},
			wantConfigNil: true,
		},
		{
			name:              "ThinkingLevel high",
			hints:             &GeminiHints{ThinkingLevel: ptr("high")},
			wantThinkingLevel: genai.ThinkingLevelHigh,
		},
		{
			name:              "ThinkingLevel minimal",
			hints:             &GeminiHints{ThinkingLevel: ptr("minimal")},
			wantThinkingLevel: genai.ThinkingLevelMinimal,
		},
		{
			name:              "ThinkingLevel low",
			hints:             &GeminiHints{ThinkingLevel: ptr("low")},
			wantThinkingLevel: genai.ThinkingLevelLow,
		},
		{
			name: "both ThinkingBudget and ThinkingLevel set",
			hints: &GeminiHints{
				ThinkingLevel:  ptr("medium"),
				ThinkingBudget: ptrInt32(256),
			},
			wantThinkingLevel: genai.ThinkingLevelMedium,
			wantBudget:        ptrInt32(256),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config := buildConfig(llm.MessageRequest{}, tc.hints, noMapping)

			if tc.wantConfigNil {
				if config.ThinkingConfig != nil {
					t.Errorf("expected ThinkingConfig to be nil, got %+v", config.ThinkingConfig)
				}
				return
			}

			if config.ThinkingConfig == nil {
				t.Fatal("expected ThinkingConfig to be set, got nil")
			}
			if config.ThinkingConfig.ThinkingLevel != tc.wantThinkingLevel {
				t.Errorf("ThinkingLevel: got %q, want %q", config.ThinkingConfig.ThinkingLevel, tc.wantThinkingLevel)
			}
			if tc.wantBudget != nil {
				if config.ThinkingConfig.ThinkingBudget == nil || *config.ThinkingConfig.ThinkingBudget != *tc.wantBudget {
					t.Errorf("ThinkingBudget: got %v, want %d", config.ThinkingConfig.ThinkingBudget, *tc.wantBudget)
				}
			}
		})
	}
}

// ptr returns a pointer to the given string value.
func ptr(s string) *string { return &s }

// ptrInt32 returns a pointer to the given int32 value.
func ptrInt32(n int32) *int32 { return &n }
