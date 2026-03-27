package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdkanthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/rapp992/gleipnir/internal/llm"
)

// newTestClient creates an AnthropicClient pointed at the given httptest server.
// Retries are disabled so error tests get immediate responses instead of
// waiting for the SDK's default retry count.
func newTestClient(t *testing.T, server *httptest.Server) *AnthropicClient {
	t.Helper()
	return NewClient("test-key",
		option.WithBaseURL(server.URL),
		option.WithMaxRetries(0),
	)
}

// serveJSON creates an httptest.Server that always responds with the given
// status code and JSON body.
func serveJSON(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body)) //nolint:errcheck
	}))
}

// messageRespJSON builds a minimal Anthropic API response JSON string.
func messageRespJSON(content, stopReason string, inputTokens, outputTokens int) string {
	return fmt.Sprintf(
		`{"id":"msg_test","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022",`+
			`"content":%s,"stop_reason":"%s","usage":{"input_tokens":%d,"output_tokens":%d}}`,
		content, stopReason, inputTokens, outputTokens,
	)
}

// minimalRequest returns a MessageRequest with the required fields set.
func minimalRequest() llm.MessageRequest {
	return llm.MessageRequest{
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 100,
		History: []llm.ConversationTurn{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}},
		},
	}
}

// --- Unit tests for unexported helpers ---

func TestResolveMaxTokens(t *testing.T) {
	ptr := func(n int64) *int64 { return &n }

	tests := []struct {
		name       string
		req        llm.MessageRequest
		hints      *AnthropicHints
		wantTokens int64
	}{
		{
			name:       "nil hints uses default",
			req:        llm.MessageRequest{},
			hints:      nil,
			wantTokens: defaultMaxTokens,
		},
		{
			name:       "hints with nil MaxTokens uses default",
			req:        llm.MessageRequest{},
			hints:      &AnthropicHints{},
			wantTokens: defaultMaxTokens,
		},
		{
			name:       "hints MaxTokens overrides default",
			req:        llm.MessageRequest{},
			hints:      &AnthropicHints{MaxTokens: ptr(8192)},
			wantTokens: 8192,
		},
		{
			name:       "req.MaxTokens wins over hints",
			req:        llm.MessageRequest{MaxTokens: 2048},
			hints:      &AnthropicHints{MaxTokens: ptr(8192)},
			wantTokens: 2048,
		},
		{
			name:       "req.MaxTokens wins with nil hints",
			req:        llm.MessageRequest{MaxTokens: 2048},
			hints:      nil,
			wantTokens: 2048,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveMaxTokens(tc.req, tc.hints)
			if got != tc.wantTokens {
				t.Errorf("resolveMaxTokens() = %d, want %d", got, tc.wantTokens)
			}
		})
	}
}

func TestBuildMessages_TextBlocks(t *testing.T) {
	tests := []struct {
		name     string
		history  []llm.ConversationTurn
		wantRole string
		wantText string
	}{
		{
			name: "user turn with text block",
			history: []llm.ConversationTurn{
				{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hello"}}},
			},
			wantRole: "user",
			wantText: "hello",
		},
		{
			name: "assistant turn with text block",
			history: []llm.ConversationTurn{
				{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock{Text: "world"}}},
			},
			wantRole: "assistant",
			wantText: "world",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msgs := buildMessages(tc.history)
			if len(msgs) != 1 {
				t.Fatalf("got %d messages, want 1", len(msgs))
			}
			if string(msgs[0].Role) != tc.wantRole {
				t.Errorf("role = %q, want %q", msgs[0].Role, tc.wantRole)
			}
			if len(msgs[0].Content) != 1 {
				t.Fatalf("got %d content blocks, want 1", len(msgs[0].Content))
			}
			block := msgs[0].Content[0]
			if block.OfText == nil {
				t.Fatal("expected OfText block, got nil")
			}
			if block.OfText.Text != tc.wantText {
				t.Errorf("text = %q, want %q", block.OfText.Text, tc.wantText)
			}
		})
	}
}

func TestBuildMessages_ToolCallBlocks(t *testing.T) {
	tests := []struct {
		name     string
		block    llm.ToolCallBlock
		wantID   string
		wantName string
	}{
		{
			name:     "tool call block preserved",
			block:    llm.ToolCallBlock{ID: "tu_1", Name: "search", Input: json.RawMessage(`{"query":"go test"}`)},
			wantID:   "tu_1",
			wantName: "search",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			history := []llm.ConversationTurn{
				{Role: llm.RoleAssistant, Content: []llm.ContentBlock{tc.block}},
			}
			msgs := buildMessages(history)
			if len(msgs) != 1 || len(msgs[0].Content) != 1 {
				t.Fatal("expected 1 message with 1 content block")
			}
			block := msgs[0].Content[0]
			if block.OfToolUse == nil {
				t.Fatal("expected OfToolUse block, got nil")
			}
			if block.OfToolUse.ID != tc.wantID {
				t.Errorf("ID = %q, want %q", block.OfToolUse.ID, tc.wantID)
			}
			if block.OfToolUse.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", block.OfToolUse.Name, tc.wantName)
			}
		})
	}
}

func TestBuildMessages_ToolResultBlocks(t *testing.T) {
	tests := []struct {
		name        string
		block       llm.ToolResultBlock
		wantIsError bool
	}{
		{
			name:        "with error",
			block:       llm.ToolResultBlock{ToolCallID: "tu_1", Content: "failed", IsError: true},
			wantIsError: true,
		},
		{
			name:        "without error",
			block:       llm.ToolResultBlock{ToolCallID: "tu_1", Content: "ok", IsError: false},
			wantIsError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			history := []llm.ConversationTurn{
				{Role: llm.RoleUser, Content: []llm.ContentBlock{tc.block}},
			}
			msgs := buildMessages(history)
			if len(msgs) != 1 || len(msgs[0].Content) != 1 {
				t.Fatal("expected 1 message with 1 content block")
			}
			block := msgs[0].Content[0]
			if block.OfToolResult == nil {
				t.Fatal("expected OfToolResult block, got nil")
			}
			if block.OfToolResult.ToolUseID != tc.block.ToolCallID {
				t.Errorf("ToolUseID = %q, want %q", block.OfToolResult.ToolUseID, tc.block.ToolCallID)
			}
			if block.OfToolResult.IsError.Value != tc.wantIsError {
				t.Errorf("IsError = %v, want %v", block.OfToolResult.IsError.Value, tc.wantIsError)
			}
		})
	}
}

func TestBuildTools(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"query": {"type": "string"}},
		"required": ["query"]
	}`)

	tools := []llm.ToolDefinition{
		{Name: "search", Description: "Search the web", InputSchema: schema},
	}

	result, err := buildTools(tools)
	if err != nil {
		t.Fatalf("buildTools() error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("got %d tools, want 1", len(result))
	}

	tool := result[0]
	if tool.OfTool == nil {
		t.Fatal("expected OfTool, got nil")
	}
	if tool.OfTool.Name != "search" {
		t.Errorf("Name = %q, want %q", tool.OfTool.Name, "search")
	}
	if !tool.OfTool.Description.Valid() || tool.OfTool.Description.Value != "Search the web" {
		t.Errorf("Description = %+v, want 'Search the web'", tool.OfTool.Description)
	}
	props, ok := tool.OfTool.InputSchema.Properties.(map[string]any)
	if !ok {
		t.Fatal("InputSchema.Properties is not map[string]any")
	}
	if _, hasQuery := props["query"]; !hasQuery {
		t.Error("expected 'query' property in schema")
	}
	if len(tool.OfTool.InputSchema.Required) != 1 || tool.OfTool.InputSchema.Required[0] != "query" {
		t.Errorf("Required = %v, want [query]", tool.OfTool.InputSchema.Required)
	}
}

func TestBuildMessages_EmptyHistory(t *testing.T) {
	// nil and empty slices must not panic and must return an empty slice.
	for _, history := range [][]llm.ConversationTurn{nil, {}} {
		msgs := buildMessages(history)
		if len(msgs) != 0 {
			t.Errorf("buildMessages(%v) = %d messages, want 0", history, len(msgs))
		}
	}
}

func TestBuildTools_Empty(t *testing.T) {
	// nil and empty slices must not panic and must return an empty slice.
	for _, tools := range [][]llm.ToolDefinition{nil, {}} {
		result, err := buildTools(tools)
		if err != nil {
			t.Fatalf("buildTools(%v) error: %v", tools, err)
		}
		if len(result) != 0 {
			t.Errorf("buildTools(%v) = %d tools, want 0", tools, len(result))
		}
	}
}

func TestBuildTools_EmptyDescription(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{}}`)
	tools := []llm.ToolDefinition{
		{Name: "noop", Description: "", InputSchema: schema},
	}
	result, err := buildTools(tools)
	if err != nil {
		t.Fatalf("buildTools() error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("got %d tools, want 1", len(result))
	}
	tool := result[0]
	if tool.OfTool == nil {
		t.Fatal("expected OfTool, got nil")
	}
	// Description must not be set when the source description is empty.
	if tool.OfTool.Description.Valid() {
		t.Errorf("Description should not be set for empty description, got %q", tool.OfTool.Description.Value)
	}
}

func TestBuildToolInputSchema_InvalidJSON(t *testing.T) {
	_, err := buildToolInputSchema(json.RawMessage(`{not valid json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestBuildSystemBlocks_CacheControl(t *testing.T) {
	trueVal := true
	falseVal := false

	tests := []struct {
		name          string
		hints         *AnthropicHints
		wantCacheCtrl bool
	}{
		{
			name:          "nil hints — no cache control",
			hints:         nil,
			wantCacheCtrl: false,
		},
		{
			name:          "caching disabled — no cache control",
			hints:         &AnthropicHints{EnablePromptCaching: &falseVal},
			wantCacheCtrl: false,
		},
		{
			name:          "caching enabled — cache control set",
			hints:         &AnthropicHints{EnablePromptCaching: &trueVal},
			wantCacheCtrl: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := llm.MessageRequest{SystemPrompt: "be helpful"}
			blocks := buildSystemBlocks(req, tc.hints)
			if len(blocks) != 1 {
				t.Fatalf("got %d system blocks, want 1", len(blocks))
			}
			// param.IsOmitted returns true when the struct is the zero value
			// (i.e. CacheControl was not set).
			hasCacheCtrl := !param.IsOmitted(blocks[0].CacheControl)
			if hasCacheCtrl != tc.wantCacheCtrl {
				t.Errorf("CacheControl set = %v, want %v", hasCacheCtrl, tc.wantCacheCtrl)
			}
		})
	}
}

func TestTranslateStopReason(t *testing.T) {
	tests := []struct {
		sdkReason sdkanthropic.StopReason
		want      llm.StopReason
	}{
		{sdkanthropic.StopReasonEndTurn, llm.StopReasonEndTurn},
		{sdkanthropic.StopReasonToolUse, llm.StopReasonToolUse},
		{sdkanthropic.StopReasonMaxTokens, llm.StopReasonMaxTokens},
		// StopReasonStopSequence, StopReasonPauseTurn, and StopReasonRefusal
		// are not mapped and fall to StopReasonUnknown.
		{sdkanthropic.StopReasonStopSequence, llm.StopReasonUnknown},
		{sdkanthropic.StopReasonPauseTurn, llm.StopReasonUnknown},
		{"unexpected_value", llm.StopReasonUnknown},
	}

	for _, tc := range tests {
		t.Run(string(tc.sdkReason), func(t *testing.T) {
			msg := &sdkanthropic.Message{StopReason: tc.sdkReason}
			got := translateResponse(msg)
			if got.StopReason != tc.want {
				t.Errorf("StopReason = %v, want %v", got.StopReason, tc.want)
			}
		})
	}
}

// --- Integration tests using httptest ---

func TestCreateMessage_TextResponse(t *testing.T) {
	tests := []struct {
		name     string
		respBody string
		wantText string
	}{
		{
			name:     "single text block",
			respBody: messageRespJSON(`[{"type":"text","text":"hello"}]`, "end_turn", 10, 5),
			wantText: "hello",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := serveJSON(t, 200, tc.respBody)
			defer srv.Close()

			client := newTestClient(t, srv)
			resp, err := client.CreateMessage(context.Background(), minimalRequest())
			if err != nil {
				t.Fatalf("CreateMessage() error: %v", err)
			}
			if len(resp.Text) != 1 {
				t.Fatalf("got %d text blocks, want 1", len(resp.Text))
			}
			if resp.Text[0].Text != tc.wantText {
				t.Errorf("text = %q, want %q", resp.Text[0].Text, tc.wantText)
			}
			if len(resp.ToolCalls) != 0 {
				t.Errorf("expected no tool calls, got %d", len(resp.ToolCalls))
			}
			if resp.StopReason != llm.StopReasonEndTurn {
				t.Errorf("StopReason = %v, want StopReasonEndTurn", resp.StopReason)
			}
		})
	}
}

func TestCreateMessage_ToolUseResponse(t *testing.T) {
	tests := []struct {
		name     string
		respBody string
		wantID   string
		wantName string
	}{
		{
			name: "single tool_use block",
			respBody: messageRespJSON(
				`[{"type":"tool_use","id":"tu_1","name":"search","input":{"query":"go"}}]`,
				"tool_use", 20, 10,
			),
			wantID:   "tu_1",
			wantName: "search",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := serveJSON(t, 200, tc.respBody)
			defer srv.Close()

			client := newTestClient(t, srv)
			resp, err := client.CreateMessage(context.Background(), minimalRequest())
			if err != nil {
				t.Fatalf("CreateMessage() error: %v", err)
			}
			if len(resp.ToolCalls) != 1 {
				t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
			}
			if resp.ToolCalls[0].ID != tc.wantID {
				t.Errorf("ID = %q, want %q", resp.ToolCalls[0].ID, tc.wantID)
			}
			if resp.ToolCalls[0].Name != tc.wantName {
				t.Errorf("Name = %q, want %q", resp.ToolCalls[0].Name, tc.wantName)
			}
			if resp.StopReason != llm.StopReasonToolUse {
				t.Errorf("StopReason = %v, want StopReasonToolUse", resp.StopReason)
			}
		})
	}
}

func TestCreateMessage_MixedResponse(t *testing.T) {
	body := messageRespJSON(
		`[{"type":"text","text":"thinking..."},{"type":"tool_use","id":"tu_2","name":"run","input":{}}]`,
		"tool_use", 30, 15,
	)

	srv := serveJSON(t, 200, body)
	defer srv.Close()

	client := newTestClient(t, srv)
	resp, err := client.CreateMessage(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("CreateMessage() error: %v", err)
	}
	if len(resp.Text) != 1 {
		t.Errorf("got %d text blocks, want 1", len(resp.Text))
	}
	if len(resp.ToolCalls) != 1 {
		t.Errorf("got %d tool calls, want 1", len(resp.ToolCalls))
	}
}

func TestTokenUsage(t *testing.T) {
	body := messageRespJSON(`[{"type":"text","text":"ok"}]`, "end_turn", 100, 50)

	srv := serveJSON(t, 200, body)
	defer srv.Close()

	client := newTestClient(t, srv)
	resp, err := client.CreateMessage(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("CreateMessage() error: %v", err)
	}
	if resp.Usage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", resp.Usage.OutputTokens)
	}
}

func TestValidateOptions(t *testing.T) {
	// ValidateOptions is pure validation; the SDK client created here is unused.
	client := NewClient("test-key")

	tests := []struct {
		name        string
		options     map[string]any
		wantErr     bool
		wantSubstrs []string
	}{
		{
			name:    "nil map passes",
			options: nil,
			wantErr: false,
		},
		{
			name:    "empty map passes",
			options: map[string]any{},
			wantErr: false,
		},
		{
			name:    "valid bool and int",
			options: map[string]any{"enable_prompt_caching": true, "max_tokens": 4096},
			wantErr: false,
		},
		{
			name:    "valid enable_prompt_caching only",
			options: map[string]any{"enable_prompt_caching": false},
			wantErr: false,
		},
		{
			name:    "valid max_tokens only",
			options: map[string]any{"max_tokens": 8192},
			wantErr: false,
		},
		{
			name:    "valid max_tokens as float64",
			options: map[string]any{"max_tokens": float64(4096)},
			wantErr: false,
		},
		{
			name:    "valid max_tokens as int64",
			options: map[string]any{"max_tokens": int64(4096)},
			wantErr: false,
		},
		{
			name:        "unknown key",
			options:     map[string]any{"foo": "bar"},
			wantErr:     true,
			wantSubstrs: []string{"unknown option: foo"},
		},
		{
			name:        "wrong type enable_prompt_caching",
			options:     map[string]any{"enable_prompt_caching": "yes"},
			wantErr:     true,
			wantSubstrs: []string{"enable_prompt_caching", "expected bool", "string"},
		},
		{
			name:        "wrong type max_tokens string",
			options:     map[string]any{"max_tokens": "big"},
			wantErr:     true,
			wantSubstrs: []string{"max_tokens", "expected numeric", "string"},
		},
		{
			name:        "max_tokens zero",
			options:     map[string]any{"max_tokens": 0},
			wantErr:     true,
			wantSubstrs: []string{"max_tokens", "must be positive"},
		},
		{
			name:        "max_tokens negative",
			options:     map[string]any{"max_tokens": -1},
			wantErr:     true,
			wantSubstrs: []string{"max_tokens", "must be positive"},
		},
		{
			name:        "max_tokens float64 zero",
			options:     map[string]any{"max_tokens": float64(0)},
			wantErr:     true,
			wantSubstrs: []string{"must be positive"},
		},
		{
			name:        "max_tokens float64 with fraction",
			options:     map[string]any{"max_tokens": 4096.5},
			wantErr:     true,
			wantSubstrs: []string{"max_tokens", "must be a whole number"},
		},
		{
			name:        "max_tokens negative fraction",
			options:     map[string]any{"max_tokens": -1.5},
			wantErr:     true,
			wantSubstrs: []string{"must be a whole number"},
		},
		{
			name:        "max_tokens int64 negative",
			options:     map[string]any{"max_tokens": int64(-5)},
			wantErr:     true,
			wantSubstrs: []string{"max_tokens", "must be positive"},
		},
		{
			name:        "multiple errors",
			options:     map[string]any{"foo": 1, "enable_prompt_caching": 42, "max_tokens": "nope"},
			wantErr:     true,
			wantSubstrs: []string{"unknown option: foo", "enable_prompt_caching", "max_tokens"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := client.ValidateOptions(tc.options)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			for _, substr := range tc.wantSubstrs {
				if !strings.Contains(err.Error(), substr) {
					t.Errorf("error %q does not contain %q", err.Error(), substr)
				}
			}
		})
	}
}

func TestCreateMessage_ErrorResponses(t *testing.T) {
	errorJSON := `{"type":"error","error":{"type":"api_error","message":"error"}}`

	tests := []struct {
		name       string
		status     int
		wantSubstr string
	}{
		{name: "rate limited", status: 429, wantSubstr: "rate limited"},
		{name: "auth failed", status: 401, wantSubstr: "authentication failed"},
		{name: "server error", status: 500, wantSubstr: "server error"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := serveJSON(t, tc.status, errorJSON)
			defer srv.Close()

			client := newTestClient(t, srv)
			_, err := client.CreateMessage(context.Background(), minimalRequest())
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}
