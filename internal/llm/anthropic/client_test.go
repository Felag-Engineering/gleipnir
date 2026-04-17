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
		name           string
		req            llm.MessageRequest
		hints          *AnthropicHints
		thinkingEnabled bool
		wantTokens     int64
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
		{
			name:            "thinking enabled uses higher default",
			req:             llm.MessageRequest{},
			hints:           nil,
			thinkingEnabled: true,
			wantTokens:      defaultMaxTokensThinking,
		},
		{
			name:            "explicit req.MaxTokens overrides thinking default",
			req:             llm.MessageRequest{MaxTokens: 2048},
			hints:           nil,
			thinkingEnabled: true,
			wantTokens:      2048,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveMaxTokens(tc.req, tc.hints, tc.thinkingEnabled)
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
			msgs := buildMessages(tc.history, nil)
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
			msgs := buildMessages(history, nil)
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
			msgs := buildMessages(history, nil)
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

	result, names, err := buildTools(tools)
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
	// Name map must map sanitized name back to original.
	if names.SanitizedToOriginal["search"] != "search" {
		t.Errorf("SanitizedToOriginal[\"search\"] = %q, want %q", names.SanitizedToOriginal["search"], "search")
	}
}

func TestBuildMessages_EmptyHistory(t *testing.T) {
	// nil and empty slices must not panic and must return an empty slice.
	for _, history := range [][]llm.ConversationTurn{nil, {}} {
		msgs := buildMessages(history, nil)
		if len(msgs) != 0 {
			t.Errorf("buildMessages(%v) = %d messages, want 0", history, len(msgs))
		}
	}
}

func TestBuildTools_Empty(t *testing.T) {
	// nil and empty slices must not panic and must return an empty slice.
	for _, tools := range [][]llm.ToolDefinition{nil, {}} {
		result, names, err := buildTools(tools)
		if err != nil {
			t.Fatalf("buildTools(%v) error: %v", tools, err)
		}
		if len(result) != 0 {
			t.Errorf("buildTools(%v) = %d tools, want 0", tools, len(result))
		}
		if len(names.SanitizedToOriginal) != 0 {
			t.Errorf("buildTools(%v) SanitizedToOriginal len = %d, want 0", tools, len(names.SanitizedToOriginal))
		}
		if len(names.OriginalToSanitized) != 0 {
			t.Errorf("buildTools(%v) OriginalToSanitized len = %d, want 0", tools, len(names.OriginalToSanitized))
		}
	}
}

func TestBuildTools_EmptyDescription(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{}}`)
	tools := []llm.ToolDefinition{
		{Name: "noop", Description: "", InputSchema: schema},
	}
	result, _, err := buildTools(tools)
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

func TestBuildTools_SanitizesNames(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{}}`)
	tools := []llm.ToolDefinition{
		{Name: "my-server.read.data", Description: "reads data", InputSchema: schema},
	}

	result, names, err := buildTools(tools)
	if err != nil {
		t.Fatalf("buildTools() error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("got %d tools, want 1", len(result))
	}
	// The tool name passed to the SDK must be sanitized.
	wantSanitized := "my-server_read_data"
	if result[0].OfTool == nil || result[0].OfTool.Name != wantSanitized {
		t.Errorf("tool name = %q, want %q", result[0].OfTool.Name, wantSanitized)
	}
	// The name map must map sanitized back to original.
	if names.SanitizedToOriginal[wantSanitized] != "my-server.read.data" {
		t.Errorf("SanitizedToOriginal[%q] = %q, want %q", wantSanitized, names.SanitizedToOriginal[wantSanitized], "my-server.read.data")
	}
}

func TestBuildTools_CollisionDetected(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{}}`)
	// Both "my.tool" and "my_tool" sanitize to "my_tool".
	tools := []llm.ToolDefinition{
		{Name: "my.tool", InputSchema: schema},
		{Name: "my_tool", InputSchema: schema},
	}

	_, _, err := buildTools(tools)
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
	if !strings.Contains(err.Error(), "collision") {
		t.Errorf("error %q does not mention 'collision'", err.Error())
	}
}

func TestTranslateResponse_ReverseMapToolName(t *testing.T) {
	// Build a fake Anthropic message with a sanitized tool name.
	body := messageRespJSON(
		`[{"type":"tool_use","id":"tu_1","name":"my-server_read_data","input":{}}]`,
		"tool_use", 10, 5,
	)
	srv := serveJSON(t, 200, body)
	defer srv.Close()

	// Register the reverse map: sanitized → original.
	// We do this by providing a tool definition with the dot-separated name,
	// which triggers sanitization inside buildTools.
	schema := json.RawMessage(`{"type":"object","properties":{}}`)
	req := llm.MessageRequest{
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 100,
		History: []llm.ConversationTurn{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}},
		},
		Tools: []llm.ToolDefinition{
			{Name: "my-server.read_data", Description: "reads data", InputSchema: schema},
		},
	}

	client := newTestClient(t, srv)
	resp, err := client.CreateMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateMessage() error: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
	}
	// The name in the response must be the original MCP name, not sanitized.
	wantName := "my-server.read_data"
	if resp.ToolCalls[0].Name != wantName {
		t.Errorf("ToolCallBlock.Name = %q, want %q", resp.ToolCalls[0].Name, wantName)
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
			got := translateResponse(msg, nil)
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

// serveSSE creates an httptest.Server that responds with a text/event-stream body.
// Each element of events is an SSE event line pair: "event: <type>\ndata: <json>\n\n".
func serveSSE(t *testing.T, events []string) *httptest.Server {
	t.Helper()
	body := strings.Join(events, "") + "\n"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte(body)) //nolint:errcheck
	}))
}

// sseEvent formats a single SSE event block.
func sseEvent(eventType, data string) string {
	return fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, data)
}

func TestStreamMessage_HappyPath(t *testing.T) {
	// Build an SSE stream with text + tool call, matching the Anthropic wire format.
	events := []string{
		sseEvent("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-3-5-sonnet-20241022","stop_reason":null,"usage":{"input_tokens":30,"output_tokens":0}}}`),
		sseEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		sseEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`),
		sseEvent("content_block_stop", `{"type":"content_block_stop","index":0}`),
		sseEvent("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_2","name":"run","input":{}}}`),
		sseEvent("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`),
		sseEvent("content_block_stop", `{"type":"content_block_stop","index":1}`),
		sseEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":15}}`),
		sseEvent("message_stop", `{"type":"message_stop"}`),
	}

	srv := serveSSE(t, events)
	defer srv.Close()

	client := newTestClient(t, srv)
	ch, err := client.StreamMessage(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("StreamMessage() error: %v", err)
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
		t.Fatalf("chunks[0].Err = %v, want nil", textChunk.Err)
	}
	if textChunk.Text == nil || *textChunk.Text != "Hello" {
		t.Errorf("chunks[0].Text = %v, want %q", textChunk.Text, "Hello")
	}
	if textChunk.StopReason != nil {
		t.Error("chunks[0].StopReason should be nil (not the final chunk)")
	}

	toolChunk := chunks[1]
	if toolChunk.ToolCall == nil {
		t.Fatal("chunks[1].ToolCall is nil, want non-nil")
	}
	if toolChunk.ToolCall.ID != "tu_2" {
		t.Errorf("chunks[1].ToolCall.ID = %q, want %q", toolChunk.ToolCall.ID, "tu_2")
	}
	if toolChunk.ToolCall.Name != "run" {
		t.Errorf("chunks[1].ToolCall.Name = %q, want %q", toolChunk.ToolCall.Name, "run")
	}

	final := chunks[2]
	if final.StopReason == nil || *final.StopReason != llm.StopReasonToolUse {
		t.Errorf("final.StopReason = %v, want StopReasonToolUse", final.StopReason)
	}
	if final.Usage == nil || final.Usage.InputTokens != 30 || final.Usage.OutputTokens != 15 {
		t.Errorf("final.Usage = %v, want {InputTokens:30, OutputTokens:15}", final.Usage)
	}
}

func TestStreamMessage_ChannelClosed(t *testing.T) {
	events := []string{
		sseEvent("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-3-5-sonnet-20241022","stop_reason":null,"usage":{"input_tokens":5,"output_tokens":0}}}`),
		sseEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		sseEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`),
		sseEvent("content_block_stop", `{"type":"content_block_stop","index":0}`),
		sseEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":3}}`),
		sseEvent("message_stop", `{"type":"message_stop"}`),
	}

	srv := serveSSE(t, events)
	defer srv.Close()

	client := newTestClient(t, srv)
	ch, err := client.StreamMessage(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("StreamMessage() error: %v", err)
	}

	// Drain all chunks before checking closure.
	for range ch {
	}

	// Channel must be closed — subsequent receive must return ok=false.
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after all chunks consumed, but received a value")
	}
}

func TestStreamMessage_HTTPErrorBeforeStream(t *testing.T) {
	// 401 response: the SDK surfaces this as a stream error (Err chunk), not a
	// synchronous return error. The NewStreaming call itself returns without
	// error; the error surfaces on the first Next() call.
	errorJSON := `{"type":"error","error":{"type":"authentication_error","message":"authentication error"}}`

	srv := serveJSON(t, 401, errorJSON)
	defer srv.Close()

	client := newTestClient(t, srv)
	ch, err := client.StreamMessage(context.Background(), minimalRequest())

	// The real streaming path: pre-stream HTTP errors arrive as Err chunks.
	// Accept either a synchronous error or an Err chunk on the channel.
	if err != nil {
		// Synchronous path is acceptable.
		if !strings.Contains(err.Error(), "authentication") {
			t.Errorf("error %q does not contain %q", err.Error(), "authentication")
		}
		return
	}

	// Async path: drain channel and check for Err chunk.
	if ch == nil {
		t.Fatal("expected non-nil channel or synchronous error")
	}
	var errChunk *llm.MessageChunk
	for c := range ch {
		if c.Err != nil {
			cp := c
			errChunk = &cp
			break
		}
	}
	// Drain remaining.
	for range ch {
	}
	if errChunk == nil {
		t.Fatal("expected an Err chunk from HTTP 401 response, got none")
	}
	if !strings.Contains(errChunk.Err.Error(), "authentication") {
		t.Errorf("Err chunk error %q does not contain %q", errChunk.Err.Error(), "authentication")
	}
}

func TestStreamMessage_ContextCancellation(t *testing.T) {
	// Pre-cancel the context. The SDK may either return synchronously or
	// emit a cancellation error chunk — both are valid outcomes.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	srv := serveSSE(t, []string{
		sseEvent("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-3-5-sonnet-20241022","stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`),
	})
	defer srv.Close()

	client := newTestClient(t, srv)
	ch, err := client.StreamMessage(ctx, minimalRequest())

	if err != nil {
		// Synchronous cancellation is acceptable.
		return
	}
	if ch == nil {
		t.Fatal("expected non-nil channel or synchronous error")
	}
	// Drain; the channel should close promptly.
	for range ch {
	}
}

func TestHumanizeModelName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude-opus-4-6", "Claude Opus 4.6"},
		{"claude-sonnet-4-6", "Claude Sonnet 4.6"},
		{"claude-haiku-4-5", "Claude Haiku 4.5"},
		{"claude-haiku-4-5-20251001", "Claude Haiku 4.5"},
		{"claude-3-5-sonnet-20241022", "Claude 3 5.sonnet"}, // ugly but irrelevant — old models are excluded by filterAnthropicFeatured
		{"not-a-claude-model", "not-a-claude-model"},
		{"claude-opus", "claude-opus"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := humanizeModelName(tt.input)
			if got != tt.want {
				t.Errorf("humanizeModelName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTranslateResponse_ThinkingBlock(t *testing.T) {
	body := messageRespJSON(
		`[{"type":"thinking","thinking":"my reasoning","signature":"sig"}]`,
		"end_turn", 10, 5,
	)
	srv := serveJSON(t, 200, body)
	defer srv.Close()

	client := newTestClient(t, srv)
	resp, err := client.CreateMessage(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("CreateMessage() error: %v", err)
	}
	if len(resp.Thinking) != 1 {
		t.Fatalf("got %d thinking blocks, want 1", len(resp.Thinking))
	}
	if resp.Thinking[0].Text != "my reasoning" {
		t.Errorf("Thinking[0].Text = %q, want %q", resp.Thinking[0].Text, "my reasoning")
	}
	if resp.Thinking[0].Redacted {
		t.Errorf("Thinking[0].Redacted = true, want false")
	}
	if len(resp.Text) != 0 {
		t.Errorf("got %d text blocks, want 0", len(resp.Text))
	}
}

func TestTranslateResponse_RedactedThinkingBlock(t *testing.T) {
	body := messageRespJSON(
		`[{"type":"redacted_thinking","data":"abc"}]`,
		"end_turn", 10, 5,
	)
	srv := serveJSON(t, 200, body)
	defer srv.Close()

	client := newTestClient(t, srv)
	resp, err := client.CreateMessage(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("CreateMessage() error: %v", err)
	}
	if len(resp.Thinking) != 1 {
		t.Fatalf("got %d thinking blocks, want 1", len(resp.Thinking))
	}
	if resp.Thinking[0].Text != "[redacted]" {
		t.Errorf("Thinking[0].Text = %q, want %q", resp.Thinking[0].Text, "[redacted]")
	}
	if !resp.Thinking[0].Redacted {
		t.Errorf("Thinking[0].Redacted = false, want true")
	}
}

func TestCreateMessage_ThinkingBlocks(t *testing.T) {
	body := messageRespJSON(
		`[{"type":"thinking","thinking":"step by step","signature":"sig1"},{"type":"text","text":"the answer"}]`,
		"end_turn", 20, 15,
	)
	srv := serveJSON(t, 200, body)
	defer srv.Close()

	client := newTestClient(t, srv)
	resp, err := client.CreateMessage(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("CreateMessage() error: %v", err)
	}
	if len(resp.Thinking) != 1 {
		t.Fatalf("got %d thinking blocks, want 1", len(resp.Thinking))
	}
	if resp.Thinking[0].Text != "step by step" {
		t.Errorf("Thinking[0].Text = %q, want %q", resp.Thinking[0].Text, "step by step")
	}
	if resp.Thinking[0].Redacted {
		t.Errorf("Thinking[0].Redacted = true, want false")
	}
	if len(resp.Text) != 1 {
		t.Fatalf("got %d text blocks, want 1", len(resp.Text))
	}
	if resp.Text[0].Text != "the answer" {
		t.Errorf("Text[0].Text = %q, want %q", resp.Text[0].Text, "the answer")
	}
}

// TestBuildTools_ReturnsOriginalToSanitized verifies that buildTools returns
// a forward map (original MCP name → sanitized wire name) alongside the
// existing reverse map, so callers can look up wire names without re-deriving.
func TestBuildTools_ReturnsOriginalToSanitized(t *testing.T) {
	tools := []llm.ToolDefinition{
		{
			Name:        "test-server.echo",
			Description: "echoes input",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		{
			Name:        "simple_tool",
			Description: "no dots",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
	}

	_, names, err := buildTools(tools)
	if err != nil {
		t.Fatalf("buildTools: %v", err)
	}

	// Forward map: original → sanitized
	if got, ok := names.OriginalToSanitized["test-server.echo"]; !ok || got != "test-server_echo" {
		t.Errorf("OriginalToSanitized[test-server.echo] = %q, ok=%v; want %q", got, ok, "test-server_echo")
	}
	if got, ok := names.OriginalToSanitized["simple_tool"]; !ok || got != "simple_tool" {
		t.Errorf("OriginalToSanitized[simple_tool] = %q, ok=%v; want %q", got, ok, "simple_tool")
	}

	// Reverse map still works
	if got, ok := names.SanitizedToOriginal["test-server_echo"]; !ok || got != "test-server.echo" {
		t.Errorf("SanitizedToOriginal[test-server_echo] = %q, ok=%v; want %q", got, ok, "test-server.echo")
	}
}

// TestBuildMessages_UsesNameMap verifies that buildMessages looks up tool
// names in the provided originalToSanitized map to produce wire-format names,
// rather than re-deriving them via sanitizeToolName. See issue #413.
func TestBuildMessages_UsesNameMap(t *testing.T) {
	tests := []struct {
		name     string
		toolName string // original MCP name in history
		nameMap  map[string]string
		wantName string
	}{
		{
			name:     "dotted MCP name looked up in map",
			toolName: "test-server.echo",
			nameMap:  map[string]string{"test-server.echo": "test-server_echo"},
			wantName: "test-server_echo",
		},
		{
			name:     "already clean name looked up in map",
			toolName: "simple_tool",
			nameMap:  map[string]string{"simple_tool": "simple_tool"},
			wantName: "simple_tool",
		},
		{
			name:     "multiple dots",
			toolName: "ns.sub.tool",
			nameMap:  map[string]string{"ns.sub.tool": "ns_sub_tool"},
			wantName: "ns_sub_tool",
		},
		{
			name:     "clean name not in map falls back unchanged",
			toolName: "unknown_tool",
			nameMap:  map[string]string{},
			wantName: "unknown_tool",
		},
		{
			name:     "dotted name not in map falls back to sanitized",
			toolName: "removed-server.old_tool",
			nameMap:  map[string]string{},
			wantName: "removed-server_old_tool",
		},
		{
			name:     "nil map passes name through unchanged",
			toolName: "any.tool",
			nameMap:  nil,
			wantName: "any.tool",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			history := []llm.ConversationTurn{
				{
					Role: llm.RoleAssistant,
					Content: []llm.ContentBlock{
						llm.ToolCallBlock{
							ID:    "tu_1",
							Name:  tc.toolName,
							Input: json.RawMessage(`{"key":"value"}`),
						},
					},
				},
			}
			msgs := buildMessages(history, tc.nameMap)
			if len(msgs) != 1 || len(msgs[0].Content) != 1 {
				t.Fatal("expected 1 message with 1 content block")
			}
			block := msgs[0].Content[0]
			if block.OfToolUse == nil {
				t.Fatal("expected OfToolUse block, got nil")
			}
			if block.OfToolUse.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", block.OfToolUse.Name, tc.wantName)
			}
		})
	}
}

// TestToolNameRoundTrip exercises the full round-trip that triggers issue #413:
// buildTools (sanitize + produce both maps) -> API response (sanitized names)
// -> translateResponse (reverse-map to original) -> store in history
// -> buildMessages (forward-map via lookup) -> verify names match registered tools.
func TestToolNameRoundTrip(t *testing.T) {
	// 1. Build tools with a dotted MCP name.
	tools := []llm.ToolDefinition{
		{
			Name:        "test-server.echo",
			Description: "echoes input",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`),
		},
	}
	toolParams, names, err := buildTools(tools)
	if err != nil {
		t.Fatalf("buildTools: %v", err)
	}

	// Verify the tool was registered with the sanitized name.
	if len(toolParams) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(toolParams))
	}
	registeredName := toolParams[0].OfTool.Name
	if registeredName != "test-server_echo" {
		t.Fatalf("registered tool name = %q, want %q", registeredName, "test-server_echo")
	}

	// 2. Simulate API response via a test HTTP server returning the sanitized name.
	body := messageRespJSON(
		`[{"type":"tool_use","id":"toolu_01ABC","name":"test-server_echo","input":{"msg":"hello"}}]`,
		"tool_use", 10, 5,
	)
	srv := serveJSON(t, 200, body)
	defer srv.Close()

	client := newTestClient(t, srv)
	resp, err := client.CreateMessage(context.Background(), llm.MessageRequest{
		Model:     "claude-3-5-sonnet-20241022",
		MaxTokens: 100,
		History: []llm.ConversationTurn{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}},
		},
		Tools: tools,
	})
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	// 3. translateResponse should have reverse-mapped to the original MCP name.
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "test-server.echo" {
		t.Fatalf("translateResponse name = %q, want %q", resp.ToolCalls[0].Name, "test-server.echo")
	}

	// 4. Store in history (as the agent loop does).
	history := []llm.ConversationTurn{
		{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{resp.ToolCalls[0]},
		},
	}

	// 5. Rebuild messages for the next API call using the forward map.
	msgs := buildMessages(history, names.OriginalToSanitized)
	if len(msgs) != 1 || len(msgs[0].Content) != 1 {
		t.Fatal("expected 1 message with 1 content block")
	}
	block := msgs[0].Content[0]
	if block.OfToolUse == nil {
		t.Fatal("expected OfToolUse block, got nil")
	}

	// 6. The name in the reconstructed message MUST match the registered tool name.
	if block.OfToolUse.Name != registeredName {
		t.Errorf("round-trip name = %q, want %q (registered name); this would cause a 400 from the API", block.OfToolUse.Name, registeredName)
	}

	// Verify both maps are consistent.
	if _, ok := names.SanitizedToOriginal[block.OfToolUse.Name]; !ok {
		t.Errorf("rebuilt name %q not found in SanitizedToOriginal map", block.OfToolUse.Name)
	}
}

func TestBuildMessages_IgnoresProviderMetadata(t *testing.T) {
	// A ToolCallBlock with Google-specific metadata must produce the same
	// Anthropic wire payload as one without. The Anthropic translator reads
	// only ID, Name, and Input.
	input := json.RawMessage(`{"q":"test"}`)

	withMeta := llm.ToolCallBlock{
		ID:    "tu_1",
		Name:  "search",
		Input: input,
		ProviderMetadata: map[string][]byte{
			"google.thought_signature": {0xde, 0xad},
		},
	}
	withoutMeta := llm.ToolCallBlock{
		ID:    "tu_1",
		Name:  "search",
		Input: input,
	}

	histWith := []llm.ConversationTurn{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{withMeta}},
	}
	histWithout := []llm.ConversationTurn{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{withoutMeta}},
	}

	msgsWith := buildMessages(histWith, nil)
	msgsWithout := buildMessages(histWithout, nil)

	if len(msgsWith) != 1 || len(msgsWithout) != 1 {
		t.Fatal("expected 1 message for each history")
	}
	blockWith := msgsWith[0].Content[0]
	blockWithout := msgsWithout[0].Content[0]

	if blockWith.OfToolUse == nil || blockWithout.OfToolUse == nil {
		t.Fatal("expected OfToolUse blocks")
	}
	if blockWith.OfToolUse.ID != blockWithout.OfToolUse.ID {
		t.Errorf("ID mismatch: %q vs %q", blockWith.OfToolUse.ID, blockWithout.OfToolUse.ID)
	}
	if blockWith.OfToolUse.Name != blockWithout.OfToolUse.Name {
		t.Errorf("Name mismatch: %q vs %q", blockWith.OfToolUse.Name, blockWithout.OfToolUse.Name)
	}
}

func TestTranslateResponse_ThinkingBlock_CapturesSignature(t *testing.T) {
	body := messageRespJSON(
		`[{"type":"thinking","thinking":"let me think","signature":"sig_abc"}]`,
		"end_turn", 10, 5,
	)
	srv := serveJSON(t, 200, body)
	defer srv.Close()

	client := newTestClient(t, srv)
	resp, err := client.CreateMessage(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if len(resp.Thinking) != 1 {
		t.Fatalf("Thinking len = %d, want 1", len(resp.Thinking))
	}
	tb := resp.Thinking[0]
	if tb.Signature != "sig_abc" {
		t.Errorf("Signature = %q, want %q", tb.Signature, "sig_abc")
	}
	if tb.Text != "let me think" {
		t.Errorf("Text = %q, want %q", tb.Text, "let me think")
	}
	if tb.Redacted {
		t.Error("Redacted should be false for a thinking block")
	}
}

func TestTranslateResponse_RedactedThinkingBlock_CapturesData(t *testing.T) {
	body := messageRespJSON(
		`[{"type":"redacted_thinking","data":"opaque_data_xyz"}]`,
		"end_turn", 10, 5,
	)
	srv := serveJSON(t, 200, body)
	defer srv.Close()

	client := newTestClient(t, srv)
	resp, err := client.CreateMessage(context.Background(), minimalRequest())
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if len(resp.Thinking) != 1 {
		t.Fatalf("Thinking len = %d, want 1", len(resp.Thinking))
	}
	tb := resp.Thinking[0]
	if tb.RedactedData != "opaque_data_xyz" {
		t.Errorf("RedactedData = %q, want %q", tb.RedactedData, "opaque_data_xyz")
	}
	if tb.Text != "[redacted]" {
		t.Errorf("Text = %q, want [redacted]", tb.Text)
	}
	if !tb.Redacted {
		t.Error("Redacted should be true for a redacted_thinking block")
	}
}

func TestBuildMessages_ThinkingBlockRoundTrip(t *testing.T) {
	history := []llm.ConversationTurn{
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{
				llm.ThinkingBlock{Text: "reasoning", Signature: "sig123"},
				llm.TextBlock{Text: "answer"},
			},
		},
	}
	msgs := buildMessages(history, nil)
	if len(msgs) != 1 {
		t.Fatalf("msgs len = %d, want 1", len(msgs))
	}
	if len(msgs[0].Content) != 2 {
		t.Fatalf("content blocks = %d, want 2", len(msgs[0].Content))
	}

	raw, err := json.Marshal(msgs[0].Content[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, `"type":"thinking"`) {
		t.Errorf("expected type=thinking in %s", s)
	}
	if !strings.Contains(s, `"signature":"sig123"`) {
		t.Errorf("expected signature=sig123 in %s", s)
	}
	if !strings.Contains(s, `"thinking":"reasoning"`) {
		t.Errorf("expected thinking=reasoning in %s", s)
	}
}

func TestBuildMessages_RedactedThinkingBlockRoundTrip(t *testing.T) {
	history := []llm.ConversationTurn{
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{
				llm.ThinkingBlock{Text: "[redacted]", Redacted: true, RedactedData: "xyz_data"},
			},
		},
	}
	msgs := buildMessages(history, nil)
	if len(msgs) != 1 || len(msgs[0].Content) != 1 {
		t.Fatalf("expected 1 message with 1 content block")
	}

	raw, err := json.Marshal(msgs[0].Content[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, `"type":"redacted_thinking"`) {
		t.Errorf("expected type=redacted_thinking in %s", s)
	}
	if !strings.Contains(s, `"data":"xyz_data"`) {
		t.Errorf("expected data=xyz_data in %s", s)
	}
	// The audit display text must never be sent to the API.
	if strings.Contains(s, "[redacted]") {
		t.Errorf("audit display text '[redacted]' must not appear in wire payload: %s", s)
	}
}

func TestBuildMessages_ThinkingBlockNoSignature_Skipped(t *testing.T) {
	// A ThinkingBlock with an empty Signature (from a non-Anthropic provider)
	// must be silently skipped — Anthropic requires a non-empty signature.
	history := []llm.ConversationTurn{
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{
				llm.ThinkingBlock{Text: "reasoning", Signature: ""},
				llm.TextBlock{Text: "answer"},
			},
		},
	}
	msgs := buildMessages(history, nil)
	if len(msgs) != 1 {
		t.Fatalf("msgs len = %d, want 1", len(msgs))
	}
	// Only the TextBlock should remain; the ThinkingBlock is dropped.
	if len(msgs[0].Content) != 1 {
		t.Fatalf("content blocks = %d, want 1 (ThinkingBlock with empty signature should be skipped)", len(msgs[0].Content))
	}
	if msgs[0].Content[0].OfText == nil {
		t.Error("expected remaining block to be a text block")
	}
}

// serveCapturingJSON creates an httptest.Server that captures the decoded
// request body and responds with the given JSON. The captured body is stored
// in the returned pointer after the first request.
func serveCapturingJSON(t *testing.T, status int, body string, captured *map[string]json.RawMessage) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			*captured = req
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body)) //nolint:errcheck
	}))
}

func TestCreateMessage_AdaptiveThinkingForReasoningModel(t *testing.T) {
	var captured map[string]json.RawMessage
	body := messageRespJSON(`[{"type":"text","text":"answer"}]`, "end_turn", 10, 5)
	srv := serveCapturingJSON(t, 200, body, &captured)
	defer srv.Close()

	client := newTestClient(t, srv)
	req := llm.MessageRequest{
		Model: "claude-opus-4-7",
		History: []llm.ConversationTurn{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}},
		},
	}
	_, err := client.CreateMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateMessage() error: %v", err)
	}
	thinkingRaw, ok := captured["thinking"]
	if !ok || string(thinkingRaw) == "null" {
		t.Fatal("expected thinking field in request, got none")
	}
	var thinking map[string]string
	if err := json.Unmarshal(thinkingRaw, &thinking); err != nil {
		t.Fatalf("unmarshal thinking: %v", err)
	}
	if thinking["type"] != "adaptive" {
		t.Errorf("thinking.type = %q, want %q", thinking["type"], "adaptive")
	}
}

func TestCreateMessage_AdaptiveThinkingForOpus46(t *testing.T) {
	var captured map[string]json.RawMessage
	body := messageRespJSON(`[{"type":"text","text":"answer"}]`, "end_turn", 10, 5)
	srv := serveCapturingJSON(t, 200, body, &captured)
	defer srv.Close()

	client := newTestClient(t, srv)
	req := llm.MessageRequest{
		Model: "claude-opus-4-6",
		History: []llm.ConversationTurn{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}},
		},
	}
	_, err := client.CreateMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateMessage() error: %v", err)
	}
	thinkingRaw, ok := captured["thinking"]
	if !ok || string(thinkingRaw) == "null" {
		t.Fatal("expected thinking field in request for claude-opus-4-6, got none")
	}
	var thinking map[string]string
	if err := json.Unmarshal(thinkingRaw, &thinking); err != nil {
		t.Fatalf("unmarshal thinking: %v", err)
	}
	if thinking["type"] != "adaptive" {
		t.Errorf("thinking.type = %q, want %q", thinking["type"], "adaptive")
	}
}

func TestCreateMessage_NoThinkingForNonReasoningModel(t *testing.T) {
	var captured map[string]json.RawMessage
	body := messageRespJSON(`[{"type":"text","text":"answer"}]`, "end_turn", 10, 5)
	srv := serveCapturingJSON(t, 200, body, &captured)
	defer srv.Close()

	client := newTestClient(t, srv)
	// claude-haiku-4-5 has IsReasoning: false — no thinking param should be sent.
	req := llm.MessageRequest{
		Model: "claude-haiku-4-5",
		History: []llm.ConversationTurn{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}},
		},
	}
	_, err := client.CreateMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateMessage() error: %v", err)
	}
	if thinkingRaw, ok := captured["thinking"]; ok && string(thinkingRaw) != "null" {
		t.Errorf("expected no thinking field for non-reasoning model, got %s", thinkingRaw)
	}
}

func TestCreateMessage_ThinkingModelDefaultsToHigherMaxTokens(t *testing.T) {
	var captured map[string]json.RawMessage
	body := messageRespJSON(`[{"type":"text","text":"answer"}]`, "end_turn", 10, 5)
	srv := serveCapturingJSON(t, 200, body, &captured)
	defer srv.Close()

	client := newTestClient(t, srv)
	// No explicit MaxTokens — should default to defaultMaxTokensThinking.
	req := llm.MessageRequest{
		Model: "claude-opus-4-7",
		History: []llm.ConversationTurn{
			{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "hi"}}},
		},
	}
	_, err := client.CreateMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateMessage() error: %v", err)
	}
	var maxTokens int
	if err := json.Unmarshal(captured["max_tokens"], &maxTokens); err != nil {
		t.Fatalf("unmarshal max_tokens: %v", err)
	}
	if maxTokens != defaultMaxTokensThinking {
		t.Errorf("max_tokens = %d, want %d (defaultMaxTokensThinking)", maxTokens, defaultMaxTokensThinking)
	}
}
