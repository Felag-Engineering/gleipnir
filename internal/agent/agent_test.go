package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/testutil"
)

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

// makeResolvedTool builds a ResolvedTool pointing at the given MCP server URL.
func makeResolvedTool(serverURL, serverName, toolName string) mcp.ResolvedTool {
	return mcp.ResolvedTool{
		GrantedTool: model.GrantedTool{
			ServerName: serverName,
			ToolName:   toolName,
			Role:       model.CapabilityRoleTool,
			Approval:   model.ApprovalModeNone,
		},
		Client:      mcp.NewClient(serverURL),
		Description: "a test tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func TestSanitizeToolName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "dot_separator_replaced",
			input: "my-server.tool_name",
			want:  "my-server_tool_name",
		},
		{
			name:  "multiple_dots_replaced",
			input: "server.tool.with.many.dots",
			want:  "server_tool_with_many_dots",
		},
		{
			name:  "already_valid_unchanged",
			input: "already_valid-name",
			want:  "already_valid-name",
		},
		{
			name:  "spaces_replaced",
			input: "server name with spaces",
			want:  "server_name_with_spaces",
		},
		{
			name:  "truncated_to_128_chars",
			input: strings.Repeat("a", 200),
			want:  strings.Repeat("a", 128),
		},
		{
			name:  "empty_string",
			input: "",
			want:  "",
		},
		{
			name:  "all_invalid_chars",
			input: "...",
			want:  "___",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeToolName(tc.input)
			if got != tc.want {
				t.Errorf("sanitizeToolName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestNew_RequiresStateMachine(t *testing.T) {
	_, err := New(Config{
		Policy: minimalPolicy(),
		Tools:  nil,
		Audit:  NewAuditWriter(testutil.NewTestStore(t).Queries()),
		Claude: testutil.NoopAnthropicClient(),
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
			toolName:     "myserver_read_data", // sanitized form as Claude returns it
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
			toolName:     "myserver_failing_tool", // sanitized form as Claude returns it
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
			toolName:     "myserver_unreliable_tool", // sanitized form as Claude returns it
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
			toolName:     "myserver_missing_tool", // sanitized form as Claude returns it
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
			toolName:     "myserver_read_data", // sanitized form as Claude returns it
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

			s := testutil.NewTestStore(t)
			testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
			testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

			w := NewAuditWriter(s.Queries())

			policy := &model.ParsedPolicy{
				Name: "test-policy",
				Agent: model.AgentConfig{
					Task: "test task",
				},
			}

			// handleToolCall tests don't go through Run(), so we start the SM
			// at "running" to match the DB row status.
			agent, err := New(Config{
				Policy:       policy,
				Tools:        tools,
				Audit:        w,
				Claude:       testutil.NoopAnthropicClient(),
				ApprovalCh:   make(chan bool),
				StateMachine: NewRunStateMachine("run1", model.RunStatusRunning, s.Queries()),
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

// toolForRun returns a ResolvedTool for Run-level tests.
func toolForRun(serverURL, serverName, toolName string) mcp.ResolvedTool {
	return mcp.ResolvedTool{
		GrantedTool: model.GrantedTool{
			ServerName: serverName,
			ToolName:   toolName,
			Role:       model.CapabilityRoleTool,
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
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		Claude:       testutil.NewFakeAnthropicClient([]*anthropic.Message{testutil.MakeTextMessage("I completed the task.", anthropic.StopReasonEndTurn, 10, 20)}),
		Tools:        nil,
		Policy:       minimalPolicy(),
		Audit:        w,
		StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.Queries()),
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

	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	tools := []mcp.ResolvedTool{toolForRun(mcpSrv.URL, "my-server", "read_data")}

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		Claude: testutil.NewFakeAnthropicClient([]*anthropic.Message{
			testutil.MakeToolUseMessage("tu-1", "my-server_read_data", map[string]any{"arg": "x"}, 10, 5),
			testutil.MakeTextMessage("Done.", anthropic.StopReasonEndTurn, 5, 3),
		}),
		Tools:        tools,
		Policy:       minimalPolicy(),
		Audit:        w,
		StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.Queries()),
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
}

func TestRun_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling Run

	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	w := NewAuditWriter(s.Queries())
	// NoopAnthropicClient panics if called — verifies no API call is made.
	ba, err := New(Config{
		Claude:       testutil.NoopAnthropicClient(),
		Tools:        nil,
		Policy:       minimalPolicy(),
		Audit:        w,
		StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = ba.Run(ctx, "r1", "do something")
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
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
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	// Policy references a tool that is not in the MCP registry.
	p := &model.ParsedPolicy{
		Name: "test-policy",
		Agent: model.AgentConfig{
			Task: "test task",
		},
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				{Tool: "myserver.missing_tool", Approval: model.ApprovalModeNone},
			},
		},
	}

	w := NewAuditWriter(s.Queries())
	// No tools registered — myserver.missing_tool cannot be resolved.
	// NoopAnthropicClient panics if called — verifies no API call is made.
	ba, err := New(Config{
		Claude:       testutil.NoopAnthropicClient(),
		Tools:        nil,
		Policy:       p,
		Audit:        w,
		StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := ba.Run(context.Background(), "r1", "trigger")
	if runErr == nil {
		t.Fatal("expected error for missing capability, got nil")
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
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	// No tools registered, but response asks for one.
	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		Claude: testutil.NewFakeAnthropicClient([]*anthropic.Message{
			testutil.MakeToolUseMessage("tu-1", "missing-server_nonexistent", map[string]any{}, 10, 5),
		}),
		Tools:        nil,
		Policy:       minimalPolicy(),
		Audit:        w,
		StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.Queries()),
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
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	// First response uses 1000 tokens (exhausts the 100-token budget).
	// The loop continues (tool_use stop_reason) and the SECOND iteration detects
	// the budget is exhausted before making another API call.
	mcpSrv := makeToolCallServer(t, json.RawMessage(`[{"type":"text","text":"tool output"}]`), false)
	tools := []mcp.ResolvedTool{{
		GrantedTool: model.GrantedTool{
			ServerName: "my-server",
			ToolName:   "read_data",
			Role:       model.CapabilityRoleTool,
			Approval:   model.ApprovalModeNone,
		},
		Client:      mcp.NewClient(mcpSrv.URL),
		Description: "a test tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}}

	p := minimalPolicy()
	p.Agent.Limits.MaxTokensPerRun = 100

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		Claude: testutil.NewFakeAnthropicClient([]*anthropic.Message{
			testutil.MakeToolUseMessage("tu-1", "my-server_read_data", map[string]any{}, 600, 400),
			// This second response should never be reached.
			testutil.MakeTextMessage("Done.", anthropic.StopReasonEndTurn, 5, 5),
		}),
		Tools:        tools,
		Policy:       p,
		Audit:        w,
		StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.Queries()),
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
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		Claude:       testutil.NewFakeAnthropicClient([]*anthropic.Message{testutil.MakeTextMessage("Done.", anthropic.StopReasonEndTurn, 5, 5)}),
		Tools:        nil,
		Policy:       minimalPolicy(),
		Audit:        w,
		StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.Queries()),
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

	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	// Tool schema only allows "arg"; "badkey" is undeclared.
	tools := []mcp.ResolvedTool{toolForRun(fakeSrv.URL, "my-server", "read_data")}

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		Claude: testutil.NewFakeAnthropicClient([]*anthropic.Message{
			testutil.MakeToolUseMessage("tu-1", "my-server_read_data", map[string]any{"badkey": "val"}, 10, 5),
		}),
		Tools:        tools,
		Policy:       minimalPolicy(),
		Audit:        w,
		StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.Queries()),
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

	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	approvalCh := make(chan bool, 1)
	approvalCh <- false // operator rejects

	approvalTool := mcp.ResolvedTool{
		GrantedTool: model.GrantedTool{
			ServerName: "my-server",
			ToolName:   "do_thing",
			Role:       model.CapabilityRoleTool,
			Approval:   model.ApprovalModeRequired,
		},
		Client:      mcp.NewClient(fakeSrv.URL),
		Description: "a world-affecting tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"arg":{"type":"string"}}}`),
	}

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		Claude: testutil.NewFakeAnthropicClient([]*anthropic.Message{
			testutil.MakeToolUseMessage("tu-1", "my-server_do_thing", map[string]any{"arg": "v"}, 10, 5),
		}),
		Tools:        []mcp.ResolvedTool{approvalTool},
		Policy:       minimalPolicy(),
		Audit:        w,
		ApprovalCh:   approvalCh,
		StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.Queries()),
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
		// Only count actual tools/call requests, not MCP initialization handshake
		// requests (initialize, notifications/initialized) which are sent by the
		// MCP client on every CallTool invocation.
		var req map[string]any
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if err := json.Unmarshal(body, &req); err == nil {
			if method, _ := req["method"].(string); method == "tools/call" {
				mcpCallCount++
			}
		}
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

	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	// With MaxToolCallsPerRun=1: first tool call (totalToolCalls=1, 1>1=false) proceeds.
	// Second response triggers the cap (totalToolCalls=2, 2>1=true) before dispatch.
	tools := []mcp.ResolvedTool{{
		GrantedTool: model.GrantedTool{
			ServerName: "my-server",
			ToolName:   "read_data",
			Role:       model.CapabilityRoleTool,
			Approval:   model.ApprovalModeNone,
		},
		Client:      mcp.NewClient(mcpSrv.URL),
		Description: "a test tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}}

	p := minimalPolicy()
	p.Agent.Limits.MaxToolCallsPerRun = 1

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		Claude: testutil.NewFakeAnthropicClient([]*anthropic.Message{
			testutil.MakeToolUseMessage("tu-1", "my-server_read_data", map[string]any{}, 10, 5),
			testutil.MakeToolUseMessage("tu-2", "my-server_read_data", map[string]any{}, 10, 5),
			// Third response should never be reached.
			testutil.MakeTextMessage("Done.", anthropic.StopReasonEndTurn, 5, 5),
		}),
		Tools:        tools,
		Policy:       p,
		Audit:        w,
		StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.Queries()),
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

	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	// One tool call well within limits.
	tools := []mcp.ResolvedTool{{
		GrantedTool: model.GrantedTool{
			ServerName: "my-server",
			ToolName:   "read_data",
			Role:       model.CapabilityRoleTool,
			Approval:   model.ApprovalModeNone,
		},
		Client:      mcp.NewClient(mcpSrv.URL),
		Description: "a test tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}}

	p := minimalPolicy()
	p.Agent.Limits.MaxTokensPerRun = 10000
	p.Agent.Limits.MaxToolCallsPerRun = 5

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		Claude: testutil.NewFakeAnthropicClient([]*anthropic.Message{
			testutil.MakeToolUseMessage("tu-1", "my-server_read_data", map[string]any{}, 10, 5),
			testutil.MakeTextMessage("Done.", anthropic.StopReasonEndTurn, 5, 3),
		}),
		Tools:        tools,
		Policy:       p,
		Audit:        w,
		StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.Queries()),
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
		s := testutil.NewTestStore(t)
		testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
		testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel before calling Run

		w := NewAuditWriter(s.Queries())
		// NoopAnthropicClient panics if called — verifies no API call is made.
		ba, err := New(Config{
			Claude:       testutil.NoopAnthropicClient(),
			Tools:        nil,
			Policy:       minimalPolicy(),
			Audit:        w,
			StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.Queries()),
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
		s := testutil.NewTestStore(t)
		testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
		testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		blockingClient, blockingTransport := testutil.NewBlockingAnthropicClient()
		w := NewAuditWriter(s.Queries())
		ba, err := New(Config{
			Claude:       blockingClient,
			Tools:        nil,
			Policy:       minimalPolicy(),
			Audit:        w,
			StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.Queries()),
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		done := make(chan error, 1)
		go func() { done <- ba.Run(ctx, "r1", "trigger") }()

		// Wait until the blocking API call has started.
		deadline := time.Now().Add(2 * time.Second)
		for blockingTransport.Calls() == 0 {
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for blocking transport to be called")
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

		s := testutil.NewTestStore(t)
		testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
		testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Fake client returns a tool_use response on the first call, directing
		// the agent to call the slow MCP server.
		tools := []mcp.ResolvedTool{toolForRun(slowSrv.URL, "slow-server", "slow_tool")}

		w := NewAuditWriter(s.Queries())
		ba, err := New(Config{
			Claude: testutil.NewFakeAnthropicClient([]*anthropic.Message{
				testutil.MakeToolUseMessage("tu-1", "slow-server_slow_tool", map[string]any{}, 10, 5),
			}),
			Tools:        tools,
			Policy:       minimalPolicy(),
			Audit:        w,
			StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.Queries()),
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

func TestRun_ToolResultTimestamp(t *testing.T) {
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

	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	tools := []mcp.ResolvedTool{toolForRun(mcpSrv.URL, "my-server", "read_data")}

	capturingClient, capturingTransport := testutil.NewCapturingAnthropicClient([]*anthropic.Message{
		testutil.MakeToolUseMessage("tu-1", "my-server_read_data", map[string]any{"arg": "x"}, 10, 5),
		testutil.MakeTextMessage("Done.", anthropic.StopReasonEndTurn, 5, 3),
	})

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		Claude:       capturingClient,
		Tools:        tools,
		Policy:       minimalPolicy(),
		Audit:        w,
		StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := ba.Run(context.Background(), "r1", "use the tool"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Two API calls are made: the first returns a tool_use, the second returns end_turn.
	// The messages slice for the second call contains:
	//   [0] user (trigger payload)
	//   [1] assistant (tool_use response)
	//   [2] user (tool results with prepended timestamp)
	bodies := capturingTransport.CapturedBodies()
	if len(bodies) != 2 {
		t.Fatalf("expected 2 API calls, got %d", len(bodies))
	}

	// Unmarshal the second call's request body to inspect the messages.
	var secondReq struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(bodies[1], &secondReq); err != nil {
		t.Fatalf("unmarshal second request body: %v", err)
	}

	if len(secondReq.Messages) < 3 {
		t.Fatalf("second call messages: want at least 3, got %d", len(secondReq.Messages))
	}

	toolResultsTurn := secondReq.Messages[2]
	if len(toolResultsTurn.Content) < 2 {
		t.Fatalf("tool-results user turn: want at least 2 content blocks, got %d", len(toolResultsTurn.Content))
	}

	// First block must be a text block matching the timestamp pattern.
	firstBlock := toolResultsTurn.Content[0]
	if firstBlock.Type != "text" {
		t.Fatalf("content[0].type = %q, want \"text\"", firstBlock.Type)
	}
	// RFC3339Nano may include fractional seconds, e.g. T12:34:56.123456789Z
	timestampRE := regexp.MustCompile(`^\[Current time: \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z\]$`)
	if !timestampRE.MatchString(firstBlock.Text) {
		t.Errorf("content[0].text = %q, want to match %s", firstBlock.Text, timestampRE)
	}

	// Second block must be a tool result block.
	secondBlock := toolResultsTurn.Content[1]
	if secondBlock.Type != "tool_result" {
		t.Errorf("content[1].type = %q, want \"tool_result\"", secondBlock.Type)
	}
}

// makeApprovalTool builds a ResolvedTool with the given approval settings,
// pointing at the provided server URL.
func makeApprovalTool(serverURL, serverName, toolName string, approval model.ApprovalMode, timeout time.Duration, onTimeout model.OnTimeout) mcp.ResolvedTool {
	return mcp.ResolvedTool{
		GrantedTool: model.GrantedTool{
			ServerName: serverName,
			ToolName:   toolName,
			Role:       model.CapabilityRoleTool,
			Approval:   approval,
			Timeout:    timeout,
			OnTimeout:  onTimeout,
		},
		Client:      mcp.NewClient(serverURL),
		Description: "a world-affecting tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

// makeAgentWithTools is a helper that builds a BoundAgent for a running run with
// the given tools. The SM is initialised at RunStatusRunning to skip the
// pending→running transition (which Run() owns).
func makeAgentWithTools(t *testing.T, tools []mcp.ResolvedTool, approvalCh chan bool) (*BoundAgent, *db.Store, *AuditWriter) {
	t.Helper()
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	w := NewAuditWriter(s.Queries())
	ch := (<-chan bool)(approvalCh)
	ba, err := New(Config{
		Policy:       minimalPolicy(),
		Tools:        tools,
		Claude:       testutil.NoopAnthropicClient(),
		Audit:        w,
		ApprovalCh:   ch,
		StateMachine: NewRunStateMachine("run1", model.RunStatusRunning, s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ba, s, w
}

func TestBuildToolDefinitions(t *testing.T) {
	tests := []struct {
		name      string
		tools     []mcp.ResolvedTool
		wantCount int
		wantErr   bool
	}{
		{
			name:      "empty_tools_produces_empty_slice",
			tools:     nil,
			wantCount: 0,
			wantErr:   false,
		},
		{
			name: "single_tool_sanitized_name_and_description",
			tools: []mcp.ResolvedTool{
				{
					GrantedTool: model.GrantedTool{
						ServerName: "my-server",
						ToolName:   "read.data",
						Role:       model.CapabilityRoleTool,
					},
					Description: "reads some data",
					InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
				},
			},
			wantCount: 1,
			wantErr:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := testutil.NewTestStore(t)
			testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
			testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

			ba, err := New(Config{
				Policy:       minimalPolicy(),
				Tools:        tc.tools,
				Claude:       testutil.NoopAnthropicClient(),
				Audit:        NewAuditWriter(s.Queries()),
				StateMachine: NewRunStateMachine("run1", model.RunStatusRunning, s.Queries()),
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			defs, err := ba.buildToolDefinitions()
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if err != nil {
				return
			}

			if len(defs) != tc.wantCount {
				t.Errorf("len(defs) = %d, want %d", len(defs), tc.wantCount)
			}

			// For the single-tool case, verify the sanitized name and description.
			if tc.name == "single_tool_sanitized_name_and_description" && len(defs) == 1 {
				tool := defs[0]
				if tool.OfTool == nil {
					t.Fatal("OfTool is nil")
				}
				wantName := "my-server_read_data"
				if tool.OfTool.Name != wantName {
					t.Errorf("tool name = %q, want %q", tool.OfTool.Name, wantName)
				}
				if !tool.OfTool.Description.Valid() || tool.OfTool.Description.Value != "reads some data" {
					t.Errorf("tool description = %v, want 'reads some data'", tool.OfTool.Description)
				}
				// Verify the name is correctly mapped via claudeNameToInternal (built in New()).
				if internal, ok := ba.claudeNameToInternal[wantName]; !ok || internal != "my-server.read.data" {
					t.Errorf("claudeNameToInternal[%q] = %q, want %q", wantName, internal, "my-server.read.data")
				}
			}
		})
	}
}

// TestBuildToolDefinitions_InvalidSchema verifies that a tool with a non-JSON
// narrowedSchema causes buildToolDefinitions to return an error. This requires
// direct construction of toolsByName to inject a bad schema after New().
func TestBuildToolDefinitions_InvalidSchema(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	ba := &BoundAgent{
		policy:   minimalPolicy(),
		audit:    NewAuditWriter(s.Queries()),
		sm:       NewRunStateMachine("run1", model.RunStatusRunning, s.Queries()),
		messages: &testutil.NoopAnthropicClient().Messages,
		toolsByName: map[string]resolvedToolEntry{
			"bad-server.bad_tool": {
				tool: mcp.ResolvedTool{
					GrantedTool: model.GrantedTool{
						ServerName: "bad-server",
						ToolName:   "bad_tool",
					},
					Description: "bad tool",
				},
				narrowedSchema: json.RawMessage(`not valid json`),
			},
		},
		claudeNameToInternal: map[string]string{
			"bad-server_bad_tool": "bad-server.bad_tool",
		},
	}

	_, err := ba.buildToolDefinitions()
	if err == nil {
		t.Error("expected error for invalid schema, got nil")
	}
}

func TestWaitForApproval(t *testing.T) {
	t.Run("Timeout_Reject", func(t *testing.T) {
		approvalCh := make(chan bool)
		ba, _, w := makeAgentWithTools(t, nil, approvalCh)
		defer w.Close()

		entry := resolvedToolEntry{
			tool: mcp.ResolvedTool{
				GrantedTool: model.GrantedTool{
					Timeout:   10 * time.Millisecond,
					OnTimeout: model.OnTimeoutReject,
				},
			},
		}

		err := ba.waitForApproval(context.Background(), "run1", entry, "my-server.do_thing", map[string]any{})
		if err == nil {
			t.Error("expected error on timeout-reject, got nil")
		}
		if !strings.Contains(err.Error(), "approval timeout") {
			t.Errorf("error message = %q, want to contain 'approval timeout'", err.Error())
		}
	})

	t.Run("Timeout_Approve", func(t *testing.T) {
		approvalCh := make(chan bool)
		ba, _, w := makeAgentWithTools(t, nil, approvalCh)
		defer w.Close()

		entry := resolvedToolEntry{
			tool: mcp.ResolvedTool{
				GrantedTool: model.GrantedTool{
					Timeout:   10 * time.Millisecond,
					OnTimeout: model.OnTimeoutApprove,
				},
			},
		}

		err := ba.waitForApproval(context.Background(), "run1", entry, "my-server.do_thing", map[string]any{})
		if err != nil {
			t.Errorf("expected nil on timeout-approve, got: %v", err)
		}
	})

	t.Run("ContextCancelled", func(t *testing.T) {
		approvalCh := make(chan bool) // unbuffered — nothing sends
		ba, _, w := makeAgentWithTools(t, nil, approvalCh)
		defer w.Close()

		// A context with a very short deadline ensures the call will return with
		// an error regardless of whether cancellation hits the audit write or the
		// approval select.
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		entry := resolvedToolEntry{
			tool: mcp.ResolvedTool{
				GrantedTool: model.GrantedTool{}, // no timeout — blocks until approval or ctx
			},
		}

		err := ba.waitForApproval(ctx, "run1", entry, "my-server.do_thing", map[string]any{})
		if err == nil {
			t.Error("expected error on context cancellation, got nil")
		}
	})

	t.Run("Approved", func(t *testing.T) {
		approvalCh := make(chan bool, 1)
		approvalCh <- true // operator approves

		ba, _, w := makeAgentWithTools(t, nil, approvalCh)
		defer w.Close()

		entry := resolvedToolEntry{
			tool: mcp.ResolvedTool{
				GrantedTool: model.GrantedTool{},
			},
		}

		err := ba.waitForApproval(context.Background(), "run1", entry, "my-server.do_thing", map[string]any{})
		if err != nil {
			t.Errorf("expected nil on approval, got: %v", err)
		}
	})

	t.Run("Rejected", func(t *testing.T) {
		approvalCh := make(chan bool, 1)
		approvalCh <- false // operator rejects

		ba, _, w := makeAgentWithTools(t, nil, approvalCh)
		defer w.Close()

		entry := resolvedToolEntry{
			tool: mcp.ResolvedTool{
				GrantedTool: model.GrantedTool{},
			},
		}

		err := ba.waitForApproval(context.Background(), "run1", entry, "my-server.do_thing", map[string]any{})
		if err == nil {
			t.Error("expected error on rejection, got nil")
		}
		if !strings.Contains(err.Error(), "rejected") {
			t.Errorf("error message = %q, want to contain 'rejected'", err.Error())
		}
	})
}

func TestProcessContentBlocks(t *testing.T) {
	tests := []struct {
		name            string
		setupResp       func(t *testing.T) (*anthropic.Message, []mcp.ResolvedTool)
		totalToolCalls  int
		maxToolCalls    int
		wantResultCount int
		wantToolCalls   int
		wantErr         bool
		wantErrCode     string
	}{
		{
			name: "single_text_block_writes_thought_no_tool_results",
			setupResp: func(t *testing.T) (*anthropic.Message, []mcp.ResolvedTool) {
				return testutil.MakeTextMessage("thinking...", anthropic.StopReasonEndTurn, 5, 3), nil
			},
			totalToolCalls:  0,
			maxToolCalls:    0,
			wantResultCount: 0,
			wantToolCalls:   0,
			wantErr:         false,
		},
		{
			name: "single_tool_use_block_returns_one_result",
			setupResp: func(t *testing.T) (*anthropic.Message, []mcp.ResolvedTool) {
				srv := makeToolCallServer(t, json.RawMessage(`[{"type":"text","text":"result"}]`), false)
				tools := []mcp.ResolvedTool{makeResolvedTool(srv.URL, "my-server", "read_data")}
				msg := testutil.MakeToolUseMessage("tu-1", "my-server_read_data", map[string]any{}, 10, 5)
				return msg, tools
			},
			totalToolCalls:  0,
			maxToolCalls:    0,
			wantResultCount: 1,
			wantToolCalls:   1,
			wantErr:         false,
		},
		{
			name: "tool_call_limit_exceeded_returns_error",
			setupResp: func(t *testing.T) (*anthropic.Message, []mcp.ResolvedTool) {
				srv := makeToolCallServer(t, json.RawMessage(`[{"type":"text","text":"result"}]`), false)
				tools := []mcp.ResolvedTool{makeResolvedTool(srv.URL, "my-server", "read_data")}
				msg := testutil.MakeToolUseMessage("tu-1", "my-server_read_data", map[string]any{}, 10, 5)
				return msg, tools
			},
			totalToolCalls:  1, // already at 1
			maxToolCalls:    1, // cap is 1, so totalToolCalls+1 > cap
			wantResultCount: 0,
			wantToolCalls:   2, // incremented before limit check
			wantErr:         true,
			wantErrCode:     "tool_call_limit_exceeded",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, tools := tc.setupResp(t)

			s := testutil.NewTestStore(t)
			testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
			testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

			w := NewAuditWriter(s.Queries())
			ba, err := New(Config{
				Policy:       minimalPolicy(),
				Tools:        tools,
				Claude:       testutil.NoopAnthropicClient(),
				Audit:        w,
				ApprovalCh:   make(chan bool),
				StateMachine: NewRunStateMachine("run1", model.RunStatusRunning, s.Queries()),
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			tokenCost := int(resp.Usage.InputTokens + resp.Usage.OutputTokens)
			results, updatedCalls, err := ba.processContentBlocks(
				context.Background(), "run1", resp,
				tc.totalToolCalls, tc.maxToolCalls, tokenCost,
			)

			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if len(results) != tc.wantResultCount {
				t.Errorf("results count = %d, want %d", len(results), tc.wantResultCount)
			}
			if updatedCalls != tc.wantToolCalls {
				t.Errorf("updatedToolCalls = %d, want %d", updatedCalls, tc.wantToolCalls)
			}

			// For error cases with a known code, verify the audit step was written.
			if tc.wantErr && tc.wantErrCode != "" {
				if err := w.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
				steps, dbErr := s.ListRunSteps(context.Background(), "run1")
				if dbErr != nil {
					t.Fatalf("ListRunSteps: %v", dbErr)
				}
				var found bool
				for _, step := range steps {
					if step.Type == string(model.StepTypeError) {
						var content map[string]string
						if jsonErr := json.Unmarshal([]byte(step.Content), &content); jsonErr == nil {
							if content["code"] == tc.wantErrCode {
								found = true
							}
						}
					}
				}
				if !found {
					t.Errorf("expected error step with code %q, not found in audit trail", tc.wantErrCode)
				}
			}
		})
	}
}

func TestRunAPILoop_EndTurn(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		Policy:       minimalPolicy(),
		Tools:        nil,
		Claude:       testutil.NewFakeAnthropicClient([]*anthropic.Message{testutil.MakeTextMessage("all done", anthropic.StopReasonEndTurn, 10, 5)}),
		Audit:        w,
		StateMachine: NewRunStateMachine("r1", model.RunStatusRunning, s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	history := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock("go")),
	}

	err = ba.runAPILoop(context.Background(), "r1", history, nil, "system prompt")
	if err != nil {
		t.Fatalf("runAPILoop: %v", err)
	}

	// Verify the run transitioned to complete.
	run, dbErr := s.GetRun(context.Background(), "r1")
	if dbErr != nil {
		t.Fatalf("GetRun: %v", dbErr)
	}
	if run.Status != string(model.RunStatusComplete) {
		t.Errorf("run status = %q, want %q", run.Status, model.RunStatusComplete)
	}

	// Verify a complete step was written.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	steps, dbErr := s.ListRunSteps(context.Background(), "r1")
	if dbErr != nil {
		t.Fatalf("ListRunSteps: %v", dbErr)
	}
	var hasComplete bool
	for _, step := range steps {
		if step.Type == string(model.StepTypeComplete) {
			hasComplete = true
		}
	}
	if !hasComplete {
		t.Error("expected a complete step in audit trail, found none")
	}
}

func TestLogAuditError(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		Policy:       minimalPolicy(),
		Tools:        nil,
		Claude:       testutil.NoopAnthropicClient(),
		Audit:        w,
		StateMachine: NewRunStateMachine("run1", model.RunStatusRunning, s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ba.logAuditError(context.Background(), "run1", "something went wrong", "test_code")

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	steps, dbErr := s.ListRunSteps(context.Background(), "run1")
	if dbErr != nil {
		t.Fatalf("ListRunSteps: %v", dbErr)
	}
	if len(steps) != 1 {
		t.Fatalf("step count = %d, want 1", len(steps))
	}
	if steps[0].Type != string(model.StepTypeError) {
		t.Errorf("step type = %q, want %q", steps[0].Type, model.StepTypeError)
	}
	var content map[string]string
	if err := json.Unmarshal([]byte(steps[0].Content), &content); err != nil {
		t.Fatalf("unmarshal step content: %v", err)
	}
	if content["message"] != "something went wrong" {
		t.Errorf("message = %q, want %q", content["message"], "something went wrong")
	}
	if content["code"] != "test_code" {
		t.Errorf("code = %q, want %q", content["code"], "test_code")
	}
}
