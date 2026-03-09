package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
)

// fakeMessages is a test double for the Anthropic Messages API that returns
// pre-canned responses in sequence.
type fakeMessages struct {
	responses []*anthropic.Message
	calls     int
}

func (f *fakeMessages) New(ctx context.Context, body anthropic.MessageNewParams, opts ...option.RequestOption) (*anthropic.Message, error) {
	if f.calls >= len(f.responses) {
		return nil, fmt.Errorf("no more fake responses")
	}
	resp := f.responses[f.calls]
	f.calls++
	return resp, nil
}

// makeTextMessage constructs an anthropic.Message via JSON unmarshalling so that
// AsAny() (which inspects JSON.raw) works correctly in tests.
func makeTextMessage(text string, stopReason anthropic.StopReason, inputTokens, outputTokens int64) *anthropic.Message {
	raw, _ := json.Marshal(map[string]any{
		"id":            "msg_test",
		"type":          "message",
		"role":          "assistant",
		"stop_reason":   string(stopReason),
		"stop_sequence": "",
		"model":         "claude-sonnet-4-6",
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
		"usage": map[string]any{
			"input_tokens":                inputTokens,
			"output_tokens":               outputTokens,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens":     0,
			"service_tier":                "standard",
		},
	})
	var msg anthropic.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		panic("makeTextMessage: " + err.Error())
	}
	return &msg
}

// makeToolUseMessage constructs a message with a tool_use content block.
func makeToolUseMessage(toolUseID, toolName string, input map[string]any, inputTokens, outputTokens int64) *anthropic.Message {
	inputJSON, _ := json.Marshal(input)
	raw, _ := json.Marshal(map[string]any{
		"id":            "msg_test",
		"type":          "message",
		"role":          "assistant",
		"stop_reason":   "tool_use",
		"stop_sequence": "",
		"model":         "claude-sonnet-4-6",
		"content": []map[string]any{
			{
				"type":  "tool_use",
				"id":    toolUseID,
				"name":  toolName,
				"input": json.RawMessage(inputJSON),
			},
		},
		"usage": map[string]any{
			"input_tokens":                inputTokens,
			"output_tokens":               outputTokens,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens":     0,
			"service_tier":                "standard",
		},
	})
	var msg anthropic.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		panic("makeToolUseMessage: " + err.Error())
	}
	return &msg
}

// noopMessages is a stub messagesAPI that returns an error if called.
// handleToolCall never calls the Claude API, so this is safe for tool-dispatch tests.
type noopMessages struct{}

func (noopMessages) New(_ context.Context, _ anthropic.MessageNewParams, _ ...option.RequestOption) (*anthropic.Message, error) {
	panic("messagesAPI.New called unexpectedly in handleToolCall test")
}

// makeToolCallServer starts an httptest.Server that responds to tools/call
// JSON-RPC requests with the given content payload and isError flag.
func makeToolCallServer(t *testing.T, content json.RawMessage, isError bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"content": content,
				"isError": isError,
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// makeResolvedTool builds a sensor ResolvedTool pointing at the given MCP server URL.
func makeResolvedTool(serverURL, serverName, toolName string) mcp.ResolvedTool {
	return mcp.ResolvedTool{
		GrantedTool: model.GrantedTool{
			ServerName: serverName,
			ToolName:   toolName,
			Role:       model.CapabilityRoleSensor,
			Approval:   model.ApprovalModeNone,
		},
		Client:      mcp.NewClient(serverURL),
		Description: "a test tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func TestNew_RequiresStateMachine(t *testing.T) {
	_, err := New(Config{
		Policy:           minimalPolicy(),
		Tools:            nil,
		Audit:            NewAuditWriter(newTestStore(t).Queries),
		MessagesOverride: noopMessages{},
		// StateMachine intentionally omitted
	})
	if err == nil {
		t.Fatal("expected error when StateMachine is nil, got nil")
	}
}

func TestHandleToolCall(t *testing.T) {
	successContent := json.RawMessage(`[{"type":"text","text":"result data"}]`)

	tests := []struct {
		name         string
		setupTools   func(t *testing.T) []mcp.ResolvedTool
		toolName     string
		input        map[string]any
		wantNonEmpty bool // true: assert output is non-empty
		wantIsError  bool
		wantErr      bool
		wantSteps    int64
		cancelBefore bool
	}{
		{
			name: "successful_call",
			setupTools: func(t *testing.T) []mcp.ResolvedTool {
				srv := makeToolCallServer(t, successContent, false)
				return []mcp.ResolvedTool{makeResolvedTool(srv.URL, "myserver", "read_data")}
			},
			toolName:     "myserver.read_data",
			input:        map[string]any{},
			wantNonEmpty: true,
			wantIsError:  false,
			wantErr:      false,
			wantSteps:    2, // tool_call + tool_result
		},
		{
			name: "tool_level_error",
			setupTools: func(t *testing.T) []mcp.ResolvedTool {
				errorContent := json.RawMessage(`[{"type":"text","text":"tool failed"}]`)
				srv := makeToolCallServer(t, errorContent, true)
				return []mcp.ResolvedTool{makeResolvedTool(srv.URL, "myserver", "failing_tool")}
			},
			toolName:     "myserver.failing_tool",
			input:        map[string]any{},
			wantNonEmpty: true,
			wantIsError:  true,
			wantErr:      false,
			wantSteps:    2, // tool_call + tool_result (is_error=true in content)
		},
		{
			name: "transport_error",
			setupTools: func(t *testing.T) []mcp.ResolvedTool {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Error(w, "gateway error", http.StatusBadGateway)
				}))
				t.Cleanup(srv.Close)
				return []mcp.ResolvedTool{makeResolvedTool(srv.URL, "myserver", "unreliable_tool")}
			},
			toolName:     "myserver.unreliable_tool",
			input:        map[string]any{},
			wantNonEmpty: false,
			wantErr:      true,
			wantSteps:    2, // tool_call step written, then error step
		},
		{
			name: "tool_not_found",
			setupTools: func(t *testing.T) []mcp.ResolvedTool {
				return []mcp.ResolvedTool{} // no tools registered
			},
			toolName:     "myserver.missing_tool",
			input:        map[string]any{},
			wantNonEmpty: false,
			wantErr:      true,
			wantSteps:    1, // only error step written
		},
		{
			name: "context_cancelled",
			setupTools: func(t *testing.T) []mcp.ResolvedTool {
				srv := makeToolCallServer(t, successContent, false)
				return []mcp.ResolvedTool{makeResolvedTool(srv.URL, "myserver", "read_data")}
			},
			toolName:     "myserver.read_data",
			input:        map[string]any{},
			wantNonEmpty: false,
			wantErr:      true,
			// Step count is not asserted (-1 sentinel): a buffered audit queue makes
			// whether the tool_call step lands before ctx.Done() nondeterministic.
			wantSteps:    -1,
			cancelBefore: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tools := tc.setupTools(t)

			s := newTestStore(t)
			insertPolicy(t, s, "p1")
			insertRun(t, s, "run1", "p1", "running")

			w := NewAuditWriter(s.Queries)

			policy := &model.ParsedPolicy{
				Name: "test-policy",
				Agent: model.AgentConfig{
					Task: "test task",
				},
			}

			// handleToolCall tests don't go through Run(), so we start the SM
			// at "running" to match the DB row status.
			agent, err := New(Config{
				Policy:           policy,
				Tools:            tools,
				Audit:            w,
				ApprovalCh:       make(chan bool),
				StateMachine:     NewRunStateMachine("run1", model.RunStatusRunning, s.Queries),
				MessagesOverride: noopMessages{},
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			ctx := context.Background()
			if tc.cancelBefore {
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel() // cancel immediately so the first Write fails
				ctx = cancelCtx
			}

			output, isError, err := agent.handleToolCall(ctx, "run1", "use-id-1", tc.toolName, tc.input)

			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if tc.wantNonEmpty && output == "" {
				t.Errorf("expected non-empty output string, got empty")
			}
			if !tc.wantNonEmpty && output != "" {
				t.Errorf("expected empty output string, got %q", output)
			}

			if !tc.wantErr && isError != tc.wantIsError {
				t.Errorf("isError = %v, want %v", isError, tc.wantIsError)
			}

			// Flush the audit writer before counting steps.
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			if tc.wantSteps >= 0 {
				count, err := s.CountRunSteps(context.Background(), "run1")
				if err != nil {
					t.Fatalf("CountRunSteps: %v", err)
				}
				if count != tc.wantSteps {
					t.Errorf("audit steps = %d, want %d", count, tc.wantSteps)
				}
			}

			// For the successful_call case, verify the audit step content shape
			// matches the spec: tool_call must have "tool_name" and "server_id";
			// tool_result must have "tool_name" and "is_error".
			if tc.name == "successful_call" {
				steps, err := s.ListRunSteps(context.Background(), "run1")
				if err != nil {
					t.Fatalf("ListRunSteps: %v", err)
				}
				for _, step := range steps {
					var content map[string]any
					if err := json.Unmarshal([]byte(step.Content), &content); err != nil {
						t.Fatalf("unmarshal step content: %v", err)
					}
					switch step.Type {
					case string(model.StepTypeToolCall):
						if v, ok := content["tool_name"]; !ok || v == "" {
							t.Errorf("tool_call step: missing non-empty 'tool_name'; content=%s", step.Content)
						}
						if v, ok := content["server_id"]; !ok || v == "" {
							t.Errorf("tool_call step: missing non-empty 'server_id'; content=%s", step.Content)
						}
					case string(model.StepTypeToolResult):
						if _, ok := content["tool_name"]; !ok {
							t.Errorf("tool_result step: missing 'tool_name'; content=%s", step.Content)
						}
						if _, ok := content["is_error"]; !ok {
							t.Errorf("tool_result step: missing 'is_error'; content=%s", step.Content)
						}
					}
				}
			}
		})
	}
}

// sensorToolForRun returns a sensor ResolvedTool for Run-level tests.
func sensorToolForRun(serverURL, serverName, toolName string) mcp.ResolvedTool {
	return mcp.ResolvedTool{
		GrantedTool: model.GrantedTool{
			ServerName: serverName,
			ToolName:   toolName,
			Role:       model.CapabilityRoleSensor,
			Approval:   model.ApprovalModeNone,
		},
		Client:      mcp.NewClient(serverURL),
		Description: "a test tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"arg":{"type":"string"}}}`),
	}
}

// minimalPolicy returns a ParsedPolicy with no limits.
func minimalPolicy() *model.ParsedPolicy {
	return &model.ParsedPolicy{
		Name: "test-policy",
		Agent: model.AgentConfig{
			Task: "test task",
		},
	}
}

func TestRun_SingleTurnEndTurn(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "pending")

	msgs := &fakeMessages{responses: []*anthropic.Message{
		makeTextMessage("I completed the task.", anthropic.StopReasonEndTurn, 10, 20),
	}}

	w := NewAuditWriter(s.Queries)
	ba, err := New(Config{
		Claude:           &anthropic.Client{},
		Tools:            nil,
		Policy:           minimalPolicy(),
		Audit:            w,
		StateMachine:     NewRunStateMachine("r1", model.RunStatusPending, s.Queries),
		MessagesOverride: msgs,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := ba.Run(context.Background(), "r1", "do stuff"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	steps, err := s.ListRunSteps(context.Background(), "r1")
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	if len(steps) < 3 {
		t.Fatalf("want at least 3 steps (snapshot, thought, complete), got %d", len(steps))
	}
	if steps[0].Type != string(model.StepTypeCapabilitySnapshot) {
		t.Errorf("step[0].Type = %q, want %q", steps[0].Type, model.StepTypeCapabilitySnapshot)
	}
	if steps[1].Type != string(model.StepTypeThought) {
		t.Errorf("step[1].Type = %q, want %q", steps[1].Type, model.StepTypeThought)
	}
	if steps[len(steps)-1].Type != string(model.StepTypeComplete) {
		t.Errorf("last step.Type = %q, want %q", steps[len(steps)-1].Type, model.StepTypeComplete)
	}

	// Verify the DB run status is "complete".
	run, err := s.GetRun(context.Background(), "r1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != string(model.RunStatusComplete) {
		t.Errorf("run status = %q, want %q", run.Status, model.RunStatusComplete)
	}
}

func TestRun_ToolCallLoop(t *testing.T) {
	// Fake MCP server that handles tools/call.
	mcpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "tool output"}},
				"isError": false,
			},
		})
	}))
	defer mcpSrv.Close()

	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "pending")

	tools := []mcp.ResolvedTool{sensorToolForRun(mcpSrv.URL, "my-server", "read_data")}

	msgs := &fakeMessages{responses: []*anthropic.Message{
		makeToolUseMessage("tu-1", "my-server.read_data", map[string]any{"arg": "x"}, 10, 5),
		makeTextMessage("Done.", anthropic.StopReasonEndTurn, 5, 3),
	}}

	w := NewAuditWriter(s.Queries)
	ba, err := New(Config{
		Claude:           &anthropic.Client{},
		Tools:            tools,
		Policy:           minimalPolicy(),
		Audit:            w,
		StateMachine:     NewRunStateMachine("r1", model.RunStatusPending, s.Queries),
		MessagesOverride: msgs,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := ba.Run(context.Background(), "r1", "use the tool"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	steps, err := s.ListRunSteps(context.Background(), "r1")
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}

	types := make([]string, len(steps))
	for i, st := range steps {
		types[i] = st.Type
	}

	wantTypes := []string{
		string(model.StepTypeCapabilitySnapshot),
		string(model.StepTypeToolCall),
		string(model.StepTypeToolResult),
		string(model.StepTypeThought),
		string(model.StepTypeComplete),
	}
	if len(steps) != len(wantTypes) {
		t.Fatalf("step count = %d, want %d; types = %v", len(steps), len(wantTypes), types)
	}
	for i, wt := range wantTypes {
		if types[i] != wt {
			t.Errorf("step[%d].Type = %q, want %q", i, types[i], wt)
		}
	}
	if msgs.calls != 2 {
		t.Errorf("API calls = %d, want 2", msgs.calls)
	}
}

func TestRun_ContextCancellation(t *testing.T) {
	msgs := &fakeMessages{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling Run

	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "pending")

	w := NewAuditWriter(s.Queries)
	ba, err := New(Config{
		Claude:           &anthropic.Client{},
		Tools:            nil,
		Policy:           minimalPolicy(),
		Audit:            w,
		StateMachine:     NewRunStateMachine("r1", model.RunStatusPending, s.Queries),
		MessagesOverride: msgs,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = ba.Run(ctx, "r1", "do something")
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if msgs.calls != 0 {
		t.Errorf("API calls = %d, want 0", msgs.calls)
	}

	// Context-cancelled runs should still be marked failed in the DB.
	run, dbErr := s.GetRun(context.Background(), "r1")
	if dbErr != nil {
		t.Fatalf("GetRun: %v", dbErr)
	}
	if run.Status != string(model.RunStatusFailed) {
		t.Errorf("run status = %q, want %q", run.Status, model.RunStatusFailed)
	}
}

func TestRun_MissingCapabilityFailsFast(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "pending")

	// Policy references a sensor tool that is not in the MCP registry.
	p := &model.ParsedPolicy{
		Name: "test-policy",
		Agent: model.AgentConfig{
			Task: "test task",
		},
		Capabilities: model.CapabilitiesConfig{
			Sensors: []model.SensorCapability{
				{Tool: "myserver.missing_tool"},
			},
		},
	}

	// Pre-canned response to verify the Claude API is NEVER called.
	msgs := &fakeMessages{responses: []*anthropic.Message{
		makeTextMessage("Done.", anthropic.StopReasonEndTurn, 5, 5),
	}}

	w := NewAuditWriter(s.Queries)
	// No tools registered — myserver.missing_tool cannot be resolved.
	ba, err := New(Config{
		Claude:           &anthropic.Client{},
		Tools:            nil,
		Policy:           p,
		Audit:            w,
		StateMachine:     NewRunStateMachine("r1", model.RunStatusPending, s.Queries),
		MessagesOverride: msgs,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := ba.Run(context.Background(), "r1", "trigger")
	if runErr == nil {
		t.Fatal("expected error for missing capability, got nil")
	}
	if msgs.calls != 0 {
		t.Errorf("Claude API calls = %d, want 0 (should fail before any API call)", msgs.calls)
	}

	// Run must be marked failed in the DB.
	run, dbErr := s.GetRun(context.Background(), "r1")
	if dbErr != nil {
		t.Fatalf("GetRun: %v", dbErr)
	}
	if run.Status != string(model.RunStatusFailed) {
		t.Errorf("run status = %q, want %q", run.Status, model.RunStatusFailed)
	}

	// An error step with code missing_capability and the tool name in the message must exist.
	steps, err := s.ListRunSteps(context.Background(), "r1")
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	var capErrFound bool
	for _, step := range steps {
		if step.Type == string(model.StepTypeError) {
			var content map[string]string
			if err := json.Unmarshal([]byte(step.Content), &content); err == nil {
				if content["code"] == "missing_capability" && strings.Contains(content["message"], "myserver.missing_tool") {
					capErrFound = true
				}
			}
		}
	}
	if !capErrFound {
		t.Error("expected error step with code 'missing_capability' and message containing 'myserver.missing_tool'")
	}
}

func TestRun_ToolNotFound(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "pending")

	// No tools registered, but response asks for one.
	msgs := &fakeMessages{responses: []*anthropic.Message{
		makeToolUseMessage("tu-1", "missing-server.nonexistent", map[string]any{}, 10, 5),
	}}

	w := NewAuditWriter(s.Queries)
	ba, err := New(Config{
		Claude:           &anthropic.Client{},
		Tools:            nil,
		Policy:           minimalPolicy(),
		Audit:            w,
		StateMachine:     NewRunStateMachine("r1", model.RunStatusPending, s.Queries),
		MessagesOverride: msgs,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := ba.Run(context.Background(), "r1", "trigger")

	if runErr == nil {
		t.Fatal("expected error for tool not found, got nil")
	}

	steps, err := s.ListRunSteps(context.Background(), "r1")
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	var hasError bool
	for _, step := range steps {
		if step.Type == string(model.StepTypeError) {
			hasError = true
		}
	}
	if !hasError {
		t.Error("expected at least one error step, found none")
	}

	// Error path must persist "failed" status.
	run, dbErr := s.GetRun(context.Background(), "r1")
	if dbErr != nil {
		t.Fatalf("GetRun: %v", dbErr)
	}
	if run.Status != string(model.RunStatusFailed) {
		t.Errorf("run status = %q, want %q", run.Status, model.RunStatusFailed)
	}
}

func TestRun_TokenBudgetExceeded(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "pending")

	// First response uses 1000 tokens (exhausts the 100-token budget).
	// The loop continues (tool_use stop_reason) and the SECOND iteration detects
	// the budget is exhausted before making another API call.
	mcpSrv := makeToolCallServer(t, json.RawMessage(`[{"type":"text","text":"tool output"}]`), false)
	msgs := &fakeMessages{responses: []*anthropic.Message{
		makeToolUseMessage("tu-1", "my-server.read_data", map[string]any{}, 600, 400),
		// This second response should never be reached.
		makeTextMessage("Done.", anthropic.StopReasonEndTurn, 5, 5),
	}}
	tools := []mcp.ResolvedTool{{
		GrantedTool: model.GrantedTool{
			ServerName: "my-server",
			ToolName:   "read_data",
			Role:       model.CapabilityRoleSensor,
			Approval:   model.ApprovalModeNone,
		},
		Client:      mcp.NewClient(mcpSrv.URL),
		Description: "a test tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}}

	p := minimalPolicy()
	p.Agent.Limits.MaxTokensPerRun = 100

	w := NewAuditWriter(s.Queries)
	ba, err := New(Config{
		Claude:           &anthropic.Client{},
		Tools:            tools,
		Policy:           p,
		Audit:            w,
		StateMachine:     NewRunStateMachine("r1", model.RunStatusPending, s.Queries),
		MessagesOverride: msgs,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := ba.Run(context.Background(), "r1", "trigger")

	if runErr == nil {
		t.Fatal("expected token budget error, got nil")
	}

	steps, err := s.ListRunSteps(context.Background(), "r1")
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}

	var budgetErrFound bool
	for _, step := range steps {
		if step.Type == string(model.StepTypeError) {
			var content map[string]string
			if err := json.Unmarshal([]byte(step.Content), &content); err == nil {
				if content["code"] == "token_budget_exceeded" {
					budgetErrFound = true
				}
			}
		}
	}
	if !budgetErrFound {
		t.Error("expected error step with code 'TOKEN_BUDGET_EXCEEDED'")
	}
}

func TestRun_CapabilitySnapshotFirst(t *testing.T) {
	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "pending")

	msgs := &fakeMessages{responses: []*anthropic.Message{
		makeTextMessage("Done.", anthropic.StopReasonEndTurn, 5, 5),
	}}

	w := NewAuditWriter(s.Queries)
	ba, err := New(Config{
		Claude:           &anthropic.Client{},
		Tools:            nil,
		Policy:           minimalPolicy(),
		Audit:            w,
		StateMachine:     NewRunStateMachine("r1", model.RunStatusPending, s.Queries),
		MessagesOverride: msgs,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := ba.Run(context.Background(), "r1", "trigger"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	steps, err := s.ListRunSteps(context.Background(), "r1")
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	if len(steps) == 0 {
		t.Fatal("no steps written")
	}
	first := steps[0]
	if first.Type != string(model.StepTypeCapabilitySnapshot) {
		t.Errorf("first step type = %q, want %q", first.Type, model.StepTypeCapabilitySnapshot)
	}
	if first.TokenCost != 0 {
		t.Errorf("capability snapshot token cost = %d, want 0", first.TokenCost)
	}
}

func TestHandleToolCall_SchemaValidation(t *testing.T) {
	var serverCallCount int
	fakeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCallCount++
	}))
	defer fakeSrv.Close()

	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "pending")

	// Tool schema only allows "arg"; "badkey" is undeclared.
	tools := []mcp.ResolvedTool{sensorToolForRun(fakeSrv.URL, "my-server", "read_data")}

	msgs := &fakeMessages{responses: []*anthropic.Message{
		makeToolUseMessage("tu-1", "my-server.read_data", map[string]any{"badkey": "val"}, 10, 5),
	}}

	w := NewAuditWriter(s.Queries)
	ba, err := New(Config{
		Claude:           &anthropic.Client{},
		Tools:            tools,
		Policy:           minimalPolicy(),
		Audit:            w,
		StateMachine:     NewRunStateMachine("r1", model.RunStatusPending, s.Queries),
		MessagesOverride: msgs,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := ba.Run(context.Background(), "r1", "trigger")

	if runErr == nil {
		t.Fatal("expected schema validation error, got nil")
	}
	if serverCallCount > 0 {
		t.Errorf("MCP server called %d times; want 0 (schema violation blocks execution)", serverCallCount)
	}

	steps, err := s.ListRunSteps(context.Background(), "r1")
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	var schemaErrFound bool
	for _, step := range steps {
		if step.Type == string(model.StepTypeError) {
			var content map[string]string
			if err := json.Unmarshal([]byte(step.Content), &content); err == nil {
				if content["code"] == "schema_violation" {
					schemaErrFound = true
				}
			}
		}
	}
	if !schemaErrFound {
		t.Error("expected error step with code 'schema_violation'")
	}
}

func TestHandleToolCall_ApprovalRejected(t *testing.T) {
	var serverCallCount int
	fakeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCallCount++
	}))
	defer fakeSrv.Close()

	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "pending")

	approvalCh := make(chan bool, 1)
	approvalCh <- false // operator rejects

	actuatorTool := mcp.ResolvedTool{
		GrantedTool: model.GrantedTool{
			ServerName: "my-server",
			ToolName:   "do_thing",
			Role:       model.CapabilityRoleActuator,
			Approval:   model.ApprovalModeRequired,
		},
		Client:      mcp.NewClient(fakeSrv.URL),
		Description: "a world-affecting tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"arg":{"type":"string"}}}`),
	}

	msgs := &fakeMessages{responses: []*anthropic.Message{
		makeToolUseMessage("tu-1", "my-server.do_thing", map[string]any{"arg": "v"}, 10, 5),
	}}

	w := NewAuditWriter(s.Queries)
	ba, err := New(Config{
		Claude:           &anthropic.Client{},
		Tools:            []mcp.ResolvedTool{actuatorTool},
		Policy:           minimalPolicy(),
		Audit:            w,
		ApprovalCh:       approvalCh,
		StateMachine:     NewRunStateMachine("r1", model.RunStatusPending, s.Queries),
		MessagesOverride: msgs,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := ba.Run(context.Background(), "r1", "trigger")

	if runErr == nil {
		t.Fatal("expected rejection error, got nil")
	}
	if serverCallCount > 0 {
		t.Errorf("MCP server called %d times; want 0 (rejection blocks execution)", serverCallCount)
	}

	steps, err := s.ListRunSteps(context.Background(), "r1")
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	var approvalErrFound bool
	for _, step := range steps {
		if step.Type == string(model.StepTypeError) {
			var content map[string]string
			if err := json.Unmarshal([]byte(step.Content), &content); err == nil {
				if content["code"] == "approval_rejected" {
					approvalErrFound = true
				}
			}
		}
	}
	if !approvalErrFound {
		t.Error("expected error step with code 'approval_rejected'")
	}
}

func TestRun_ToolCallCapExceeded(t *testing.T) {
	var mcpCallCount int
	mcpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mcpCallCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "tool output"}},
				"isError": false,
			},
		})
	}))
	defer mcpSrv.Close()

	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "pending")

	// With MaxToolCallsPerRun=1: first tool call (totalToolCalls=1, 1>1=false) proceeds.
	// Second response triggers the cap (totalToolCalls=2, 2>1=true) before dispatch.
	msgs := &fakeMessages{responses: []*anthropic.Message{
		makeToolUseMessage("tu-1", "my-server.read_data", map[string]any{}, 10, 5),
		makeToolUseMessage("tu-2", "my-server.read_data", map[string]any{}, 10, 5),
		// Third response should never be reached.
		makeTextMessage("Done.", anthropic.StopReasonEndTurn, 5, 5),
	}}
	tools := []mcp.ResolvedTool{{
		GrantedTool: model.GrantedTool{
			ServerName: "my-server",
			ToolName:   "read_data",
			Role:       model.CapabilityRoleSensor,
			Approval:   model.ApprovalModeNone,
		},
		Client:      mcp.NewClient(mcpSrv.URL),
		Description: "a test tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}}

	p := minimalPolicy()
	p.Agent.Limits.MaxToolCallsPerRun = 1

	w := NewAuditWriter(s.Queries)
	ba, err := New(Config{
		Claude:           &anthropic.Client{},
		Tools:            tools,
		Policy:           p,
		Audit:            w,
		StateMachine:     NewRunStateMachine("r1", model.RunStatusPending, s.Queries),
		MessagesOverride: msgs,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := ba.Run(context.Background(), "r1", "trigger")

	if runErr == nil {
		t.Fatal("expected tool call cap error, got nil")
	}
	// MCP server called once: the first tool call goes through before the cap fires.
	if mcpCallCount != 1 {
		t.Errorf("MCP server called %d times, want 1", mcpCallCount)
	}

	steps, err := s.ListRunSteps(context.Background(), "r1")
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}

	var capErrFound bool
	for _, step := range steps {
		if step.Type == string(model.StepTypeError) {
			var content map[string]string
			if err := json.Unmarshal([]byte(step.Content), &content); err == nil {
				if content["code"] == "tool_call_limit_exceeded" {
					capErrFound = true
				}
			}
		}
	}
	if !capErrFound {
		t.Error("expected error step with code 'TOOL_CALL_LIMIT_EXCEEDED'")
	}
}

func TestRun_LimitsNotExceeded(t *testing.T) {
	mcpSrv := makeToolCallServer(t, json.RawMessage(`[{"type":"text","text":"ok"}]`), false)

	s := newTestStore(t)
	insertPolicy(t, s, "p1")
	insertRun(t, s, "r1", "p1", "pending")

	// One tool call well within limits.
	msgs := &fakeMessages{responses: []*anthropic.Message{
		makeToolUseMessage("tu-1", "my-server.read_data", map[string]any{}, 10, 5),
		makeTextMessage("Done.", anthropic.StopReasonEndTurn, 5, 3),
	}}
	tools := []mcp.ResolvedTool{{
		GrantedTool: model.GrantedTool{
			ServerName: "my-server",
			ToolName:   "read_data",
			Role:       model.CapabilityRoleSensor,
			Approval:   model.ApprovalModeNone,
		},
		Client:      mcp.NewClient(mcpSrv.URL),
		Description: "a test tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}}

	p := minimalPolicy()
	p.Agent.Limits.MaxTokensPerRun = 10000
	p.Agent.Limits.MaxToolCallsPerRun = 5

	w := NewAuditWriter(s.Queries)
	ba, err := New(Config{
		Claude:           &anthropic.Client{},
		Tools:            tools,
		Policy:           p,
		Audit:            w,
		StateMachine:     NewRunStateMachine("r1", model.RunStatusPending, s.Queries),
		MessagesOverride: msgs,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := ba.Run(context.Background(), "r1", "trigger"); err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}

	steps, err := s.ListRunSteps(context.Background(), "r1")
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	for _, step := range steps {
		if step.Type == string(model.StepTypeError) {
			t.Errorf("unexpected error step: %s", step.Content)
		}
	}
}

// blockingMessages is a test double for the Anthropic Messages API that blocks
// until the provided context is cancelled. It counts how many times New() has
// been called so the test can synchronise on the API call starting.
type blockingMessages struct {
	calls atomic.Int64
}

func (b *blockingMessages) New(ctx context.Context, _ anthropic.MessageNewParams, _ ...option.RequestOption) (*anthropic.Message, error) {
	b.calls.Add(1)
	// Block until the caller's context is cancelled.
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRun_Cancellation(t *testing.T) {
	// assertCancelledStep asserts that at least one error step with code "cancelled"
	// and message "run cancelled" exists in the audit trail for the given run.
	assertCancelledStep := func(t *testing.T, s *db.Store, runID string) {
		t.Helper()
		steps, err := s.ListRunSteps(context.Background(), runID)
		if err != nil {
			t.Fatalf("ListRunSteps: %v", err)
		}
		for _, step := range steps {
			if step.Type != string(model.StepTypeError) {
				continue
			}
			var content map[string]string
			if err := json.Unmarshal([]byte(step.Content), &content); err != nil {
				continue
			}
			if content["code"] == "cancelled" && content["message"] == "run cancelled" {
				return
			}
		}
		t.Errorf("no error step with code=cancelled and message='run cancelled' found in audit trail")
	}

	t.Run("cancel_before_loop", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "p1")
		insertRun(t, s, "r1", "p1", "pending")

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel before calling Run

		msgs := &fakeMessages{} // no responses needed — loop never reaches API call
		w := NewAuditWriter(s.Queries)
		ba, err := New(Config{
			Claude:           &anthropic.Client{},
			Tools:            nil,
			Policy:           minimalPolicy(),
			Audit:            w,
			StateMachine:     NewRunStateMachine("r1", model.RunStatusPending, s.Queries),
			MessagesOverride: msgs,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		done := make(chan error, 1)
		go func() { done <- ba.Run(ctx, "r1", "trigger") }()

		select {
		case runErr := <-done:
			if runErr == nil {
				t.Fatal("expected error from cancelled context, got nil")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run() goroutine did not exit within 2s after cancellation")
		}

		// DB run status must be "failed".
		run, dbErr := s.GetRun(context.Background(), "r1")
		if dbErr != nil {
			t.Fatalf("GetRun: %v", dbErr)
		}
		if run.Status != string(model.RunStatusFailed) {
			t.Errorf("run status = %q, want %q", run.Status, model.RunStatusFailed)
		}

		assertCancelledStep(t, s, "r1")
	})

	t.Run("cancel_during_api_call", func(t *testing.T) {
		s := newTestStore(t)
		insertPolicy(t, s, "p1")
		insertRun(t, s, "r1", "p1", "pending")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		blocking := &blockingMessages{}
		w := NewAuditWriter(s.Queries)
		ba, err := New(Config{
			Claude:           &anthropic.Client{},
			Tools:            nil,
			Policy:           minimalPolicy(),
			Audit:            w,
			StateMachine:     NewRunStateMachine("r1", model.RunStatusPending, s.Queries),
			MessagesOverride: blocking,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		done := make(chan error, 1)
		go func() { done <- ba.Run(ctx, "r1", "trigger") }()

		// Wait until the blocking API call has started.
		deadline := time.Now().Add(2 * time.Second)
		for blocking.calls.Load() == 0 {
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for blockingMessages.New to be called")
			}
			time.Sleep(time.Millisecond)
		}

		cancel() // cancel while the API call is blocked

		select {
		case runErr := <-done:
			if runErr == nil {
				t.Fatal("expected error from cancelled context, got nil")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run() goroutine did not exit within 2s after cancellation")
		}

		assertCancelledStep(t, s, "r1")
	})

	t.Run("cancel_during_tool_invocation", func(t *testing.T) {
		// reached is closed by the slow server as soon as it receives a request,
		// allowing the test to synchronise on the tool invocation being in-flight
		// before cancelling the context.
		reached := make(chan struct{})
		var reachedOnce sync.Once

		// srvDone is closed during cleanup to unblock the slow server handler so
		// httptest.Server.Close() can complete without hanging on active connections.
		srvDone := make(chan struct{})

		slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Signal that the server has been reached — exactly once.
			reachedOnce.Do(func() { close(reached) })
			// Block until the client disconnects or cleanup forces the handler to exit.
			select {
			case <-r.Context().Done():
			case <-srvDone:
			}
			// Don't write a response — the test only needs the block behaviour.
		}))
		t.Cleanup(func() {
			close(srvDone)
			slowSrv.Close()
		})

		s := newTestStore(t)
		insertPolicy(t, s, "p1")
		insertRun(t, s, "r1", "p1", "pending")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// fakeMessages returns a tool_use response on the first call, directing
		// the agent to call the slow MCP server.
		msgs := &fakeMessages{responses: []*anthropic.Message{
			makeToolUseMessage("tu-1", "slow-server.slow_tool", map[string]any{}, 10, 5),
		}}

		tools := []mcp.ResolvedTool{sensorToolForRun(slowSrv.URL, "slow-server", "slow_tool")}

		w := NewAuditWriter(s.Queries)
		ba, err := New(Config{
			Claude:           &anthropic.Client{},
			Tools:            tools,
			Policy:           minimalPolicy(),
			Audit:            w,
			StateMachine:     NewRunStateMachine("r1", model.RunStatusPending, s.Queries),
			MessagesOverride: msgs,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		done := make(chan error, 1)
		go func() { done <- ba.Run(ctx, "r1", "trigger") }()

		// Wait until the slow MCP server has received the request before cancelling.
		select {
		case <-reached:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for tool invocation to reach slow server")
		}

		cancel() // cancel while the tool invocation is blocked

		select {
		case runErr := <-done:
			if runErr == nil {
				t.Fatal("expected error from cancelled context, got nil")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run() goroutine did not exit within 2s after cancellation")
		}

		// The tool call was cancelled — we expect a failed run in the DB.
		run, dbErr := s.GetRun(context.Background(), "r1")
		if dbErr != nil {
			t.Fatalf("GetRun: %v", dbErr)
		}
		if run.Status != string(model.RunStatusFailed) {
			t.Errorf("run status = %q, want %q", run.Status, model.RunStatusFailed)
		}

		assertCancelledStep(t, s, "r1")
	})
}
