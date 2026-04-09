package openaicompat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rapp992/gleipnir/internal/llm"
)

func strp(s string) *string   { return &s }
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
						llm.ThinkingBlock{Text: "reasoning", Signature: "sig"},
						llm.TextBlock{Text: "answer"},
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
			req := BuildChatCompletionRequest(tc.in, false, llm.ToolNameMapping{})
			tc.check(t, req)
		})
	}
}

func TestBuildChatCompletionRequest_StreamFlag(t *testing.T) {
	req := BuildChatCompletionRequest(llm.MessageRequest{Model: "gpt-4o"}, true, llm.ToolNameMapping{})
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
		"o1":              true,
		"o1-mini":         true,
		"o3":              true,
		"o3-mini":         true,
		"o4-mini":         true,
		"gpt-5-reasoning": true,
		"gpt-4o":          false,
		"gpt-4o-mini":     false,
		"gpt-4.1":         false,
		"llama3.1:70b":    false,
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
	req := BuildChatCompletionRequest(in, false, llm.ToolNameMapping{})
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
			name:     "text only",
			fixture:  "chat_response_text_only.json",
			wantText: "Hello from OpenAI.", wantStop: llm.StopReasonEndTurn,
			wantInTok: 12, wantOutTok: 5,
		},
		{
			name:      "single tool call",
			fixture:   "chat_response_with_tool_calls.json",
			wantCalls: 1, wantStop: llm.StopReasonToolUse,
			wantInTok: 40, wantOutTok: 15,
		},
		{
			name:      "parallel tool calls",
			fixture:   "chat_response_parallel_tool_calls.json",
			wantCalls: 2, wantStop: llm.StopReasonToolUse,
			wantInTok: 50, wantOutTok: 20,
		},
		{
			name:     "o-series reasoning tokens",
			fixture:  "chat_response_o_series_with_reasoning_tokens.json",
			wantText: "42", wantStop: llm.StopReasonEndTurn,
			wantInTok: 30, wantOutTok: 150, wantThinkTk: 120,
		},
		{
			name:     "length truncation",
			fixture:  "chat_response_finish_length.json",
			wantText: "truncated...", wantStop: llm.StopReasonMaxTokens,
			wantInTok: 10, wantOutTok: 100,
		},
		{
			name:      "content filter maps to error",
			fixture:   "chat_response_finish_content_filter.json",
			wantStop:  llm.StopReasonError,
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
			resp, err := ParseChatCompletionResponse(&wire, llm.ToolNameMapping{})
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if tc.wantText != "" {
				if len(resp.Text) != 1 || resp.Text[0].Text != tc.wantText {
					t.Errorf("text: %+v", resp.Text)
				}
			}
			if tc.name == "content filter maps to error" && len(resp.Text) != 0 {
				t.Errorf("content_filter: expected no text, got %+v", resp.Text)
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
					ID:       "c1",
					Type:     "function",
					Function: chatToolCallFunc{Name: "t", Arguments: "not-json"},
				}},
			},
			FinishReason: "tool_calls",
		}},
	}
	if _, err := ParseChatCompletionResponse(wire, llm.ToolNameMapping{}); err == nil {
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
	resp, err := ParseChatCompletionResponse(wire, llm.ToolNameMapping{})
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
	resp, err := ParseChatCompletionResponse(wire, llm.ToolNameMapping{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Usage.ThinkingTokens != 0 {
		t.Errorf("thinking tokens should be 0, got %d", resp.Usage.ThinkingTokens)
	}
}

// TestToolNameSanitization_RoundTrip verifies that tool names containing
// characters outside OpenAI's allowed set (`^[a-zA-Z0-9_-]+$`) are sanitized
// on the way out and reversed on the way back — including the name that
// appears in the assistant's tool_call history entry. Without this the live
// OpenAI API returns 400 Invalid 'tools[0].function.name' for any Gleipnir
// tool that uses '.' as an MCP namespace separator.
func TestToolNameSanitization_RoundTrip(t *testing.T) {
	originalName := "gleipnir.ask_operator"
	names := llm.BuildNameMapping([]llm.ToolDefinition{{Name: originalName}}, "-")

	sanitized, ok := names.OriginalToSanitized[originalName]
	if !ok {
		t.Fatal("sanitized name missing from mapping")
	}
	// '.' is not in [a-zA-Z0-9_-], so it must have been rewritten.
	if sanitized == originalName {
		t.Fatalf("expected sanitization, got unchanged name %q", sanitized)
	}

	// Outbound: the wire tool list must carry the sanitized name, and an
	// assistant history turn referencing the tool by its original name must
	// also be rewritten.
	req := llm.MessageRequest{
		Model: "gpt-4.1-nano",
		Tools: []llm.ToolDefinition{{Name: originalName, Description: "ask"}},
		History: []llm.ConversationTurn{{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{llm.ToolCallBlock{
				ID: "call_1", Name: originalName, Input: json.RawMessage(`{}`),
			}},
		}},
	}
	wire := BuildChatCompletionRequest(req, false, names)
	if got := wire.Tools[0].Function.Name; got != sanitized {
		t.Errorf("outbound tool name = %q, want %q", got, sanitized)
	}
	if got := wire.Messages[0].ToolCalls[0].Function.Name; got != sanitized {
		t.Errorf("outbound history tool_call name = %q, want %q", got, sanitized)
	}

	// Inbound (sync): OpenAI echoes the sanitized name back; the parser must
	// reverse it to the original so the agent runtime can dispatch the call.
	resp := &chatResponse{Choices: []chatChoice{{
		Message: chatMessage{
			ToolCalls: []chatToolCall{{
				ID: "call_1", Type: "function",
				Function: chatToolCallFunc{Name: sanitized, Arguments: "{}"},
			}},
		},
		FinishReason: "tool_calls",
	}}}
	parsed, err := ParseChatCompletionResponse(resp, names)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(parsed.ToolCalls))
	}
	if parsed.ToolCalls[0].Name != originalName {
		t.Errorf("reversed name = %q, want %q", parsed.ToolCalls[0].Name, originalName)
	}
}

func TestTranslate_IgnoresProviderMetadata(t *testing.T) {
	// A ToolCallBlock carrying Google-specific metadata must produce the same
	// OpenAI-compat wire message as one without. The translator reads only
	// ID, Name, and Input.
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

	makeReq := func(block llm.ToolCallBlock) chatRequest {
		return BuildChatCompletionRequest(llm.MessageRequest{
			Model: "gpt-4o",
			History: []llm.ConversationTurn{
				{Role: llm.RoleAssistant, Content: []llm.ContentBlock{block}},
			},
		}, false, llm.ToolNameMapping{})
	}

	reqWith := makeReq(withMeta)
	reqWithout := makeReq(withoutMeta)

	if len(reqWith.Messages) != 1 || len(reqWithout.Messages) != 1 {
		t.Fatal("expected 1 message each")
	}
	mWith := reqWith.Messages[0]
	mWithout := reqWithout.Messages[0]

	if len(mWith.ToolCalls) != 1 || len(mWithout.ToolCalls) != 1 {
		t.Fatal("expected 1 tool_call each")
	}
	if mWith.ToolCalls[0].ID != mWithout.ToolCalls[0].ID {
		t.Errorf("ID mismatch: %q vs %q", mWith.ToolCalls[0].ID, mWithout.ToolCalls[0].ID)
	}
	if mWith.ToolCalls[0].Function.Name != mWithout.ToolCalls[0].Function.Name {
		t.Errorf("Name mismatch: %q vs %q", mWith.ToolCalls[0].Function.Name, mWithout.ToolCalls[0].Function.Name)
	}
	if mWith.ToolCalls[0].Function.Arguments != mWithout.ToolCalls[0].Function.Arguments {
		t.Errorf("Arguments mismatch: %q vs %q", mWith.ToolCalls[0].Function.Arguments, mWithout.ToolCalls[0].Function.Arguments)
	}
}
