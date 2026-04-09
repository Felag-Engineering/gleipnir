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

	"github.com/rapp992/gleipnir/internal/approval"
	"github.com/rapp992/gleipnir/internal/db"
	feedbackscanner "github.com/rapp992/gleipnir/internal/feedback"
	"github.com/rapp992/gleipnir/internal/llm"
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
			Approval:   model.ApprovalModeNone,
		},
		Client:      mcp.NewClient(serverURL),
		Description: "a test tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func TestNew_RequiresStateMachine(t *testing.T) {
	_, err := New(Config{
		Policy:    minimalPolicy(),
		Tools:     nil,
		Audit:     NewAuditWriter(testutil.NewTestStore(t).Queries()),
		LLMClient: testutil.NewNoopLLMClient(),
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
			toolName:     "myserver.read_data", // original MCP dot-separated name
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
			toolName:     "myserver.failing_tool", // original MCP dot-separated name
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
			toolName:     "myserver.unreliable_tool", // original MCP dot-separated name
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
			toolName:     "myserver.missing_tool", // original MCP dot-separated name
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
			toolName:     "myserver.read_data", // original MCP dot-separated name
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
				LLMClient:    testutil.NewNoopLLMClient(),
				ApprovalCh:   make(chan bool), // unbuffered — these tests don't exercise the approval gate
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

			output, isError, err := agent.handleToolCall(ctx, "run1", tc.toolName, tc.input)

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
		LLMClient:    testutil.NewMockLLMClient(testutil.MakeLLMTextResponse("I completed the task.", llm.StopReasonEndTurn, 10, 20)),
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
		LLMClient: testutil.NewMockLLMClient(
			testutil.MakeLLMToolCallResponse("tu-1", "my-server.read_data", map[string]any{"arg": "x"}, 10, 5),
			testutil.MakeLLMTextResponse("Done.", llm.StopReasonEndTurn, 5, 3),
		),
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
	// NewNoopLLMClient panics if CreateMessage is called — verifies no API call is made.
	ba, err := New(Config{
		LLMClient:    testutil.NewNoopLLMClient(),
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
	// NewNoopLLMClient panics if CreateMessage is called — verifies no API call is made.
	ba, err := New(Config{
		LLMClient:    testutil.NewNoopLLMClient(),
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
		LLMClient: testutil.NewMockLLMClient(
			testutil.MakeLLMToolCallResponse("tu-1", "missing-server.nonexistent", map[string]any{}, 10, 5),
		),
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
		LLMClient: testutil.NewMockLLMClient(
			testutil.MakeLLMToolCallResponse("tu-1", "my-server.read_data", map[string]any{}, 600, 400),
			// This second response should never be reached.
			testutil.MakeLLMTextResponse("Done.", llm.StopReasonEndTurn, 5, 5),
		),
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

	pol := minimalPolicy()
	pol.Agent.ModelConfig.Provider = "anthropic"
	pol.Agent.ModelConfig.Name = "claude-sonnet-4-6"

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		LLMClient:    testutil.NewMockLLMClient(testutil.MakeLLMTextResponse("Done.", llm.StopReasonEndTurn, 5, 5)),
		Tools:        nil,
		Policy:       pol,
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

	// Verify provider and model are recorded in the snapshot content JSON.
	type snapshotContent struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	var snap snapshotContent
	if err := json.Unmarshal([]byte(first.Content), &snap); err != nil {
		t.Fatalf("unmarshal snapshot content: %v", err)
	}
	if snap.Provider != "anthropic" {
		t.Errorf("snapshot provider = %q, want %q", snap.Provider, "anthropic")
	}
	if snap.Model != "claude-sonnet-4-6" {
		t.Errorf("snapshot model = %q, want %q", snap.Model, "claude-sonnet-4-6")
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
		LLMClient: testutil.NewMockLLMClient(
			testutil.MakeLLMToolCallResponse("tu-1", "my-server.read_data", map[string]any{"badkey": "val"}, 10, 5),
		),
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
			Approval:   model.ApprovalModeRequired,
		},
		Client:      mcp.NewClient(fakeSrv.URL),
		Description: "a world-affecting tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"arg":{"type":"string"}}}`),
	}

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		LLMClient: testutil.NewMockLLMClient(
			testutil.MakeLLMToolCallResponse("tu-1", "my-server.do_thing", map[string]any{"arg": "v"}, 10, 5),
		),
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
		LLMClient: testutil.NewMockLLMClient(
			testutil.MakeLLMToolCallResponse("tu-1", "my-server.read_data", map[string]any{}, 10, 5),
			testutil.MakeLLMToolCallResponse("tu-2", "my-server.read_data", map[string]any{}, 10, 5),
			// Third response should never be reached.
			testutil.MakeLLMTextResponse("Done.", llm.StopReasonEndTurn, 5, 5),
		),
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
		LLMClient: testutil.NewMockLLMClient(
			testutil.MakeLLMToolCallResponse("tu-1", "my-server.read_data", map[string]any{}, 10, 5),
			testutil.MakeLLMTextResponse("Done.", llm.StopReasonEndTurn, 5, 3),
		),
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
		// NewNoopLLMClient panics if CreateMessage is called — verifies no API call is made.
		ba, err := New(Config{
			LLMClient:    testutil.NewNoopLLMClient(),
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

		blockingClient, blockingTransport := testutil.NewBlockingLLMClient()
		w := NewAuditWriter(s.Queries())
		ba, err := New(Config{
			LLMClient:    blockingClient,
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
			LLMClient: testutil.NewMockLLMClient(
				testutil.MakeLLMToolCallResponse("tu-1", "slow-server.slow_tool", map[string]any{}, 10, 5),
			),
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

	mockClient := testutil.NewMockLLMClient(
		testutil.MakeLLMToolCallResponse("tu-1", "my-server.read_data", map[string]any{"arg": "x"}, 10, 5),
		testutil.MakeLLMTextResponse("Done.", llm.StopReasonEndTurn, 5, 3),
	)

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		LLMClient:    mockClient,
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
	// The history slice for the second call contains:
	//   [0] user (trigger payload)
	//   [1] assistant (tool_use response)
	//   [2] user (tool results with prepended timestamp)
	requests := mockClient.Requests()
	if len(requests) != 2 {
		t.Fatalf("expected 2 API calls, got %d", len(requests))
	}

	secondReq := requests[1]
	if len(secondReq.History) < 3 {
		t.Fatalf("second call history: want at least 3 turns, got %d", len(secondReq.History))
	}

	toolResultsTurn := secondReq.History[2]
	if len(toolResultsTurn.Content) < 2 {
		t.Fatalf("tool-results user turn: want at least 2 content blocks, got %d", len(toolResultsTurn.Content))
	}

	// First block must be a ToolResultBlock (Anthropic API requires tool_result
	// blocks before text blocks in user messages).
	if _, ok := toolResultsTurn.Content[0].(llm.ToolResultBlock); !ok {
		t.Fatalf("content[0] type = %T, want llm.ToolResultBlock", toolResultsTurn.Content[0])
	}

	// Last block must be a TextBlock matching the timestamp pattern.
	lastBlock, ok := toolResultsTurn.Content[len(toolResultsTurn.Content)-1].(llm.TextBlock)
	if !ok {
		t.Fatalf("content[last] type = %T, want llm.TextBlock", toolResultsTurn.Content[len(toolResultsTurn.Content)-1])
	}
	// RFC3339Nano may include fractional seconds, e.g. T12:34:56.123456789Z
	timestampRE := regexp.MustCompile(`^\[Current time: \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z\]$`)
	if !timestampRE.MatchString(lastBlock.Text) {
		t.Errorf("content[last].Text = %q, want to match %s", lastBlock.Text, timestampRE)
	}
}

// makeApprovalTool builds a ResolvedTool with the given approval settings,
// pointing at the provided server URL.
func makeApprovalTool(serverURL, serverName, toolName string, approval model.ApprovalMode, timeout time.Duration, onTimeout model.OnTimeout) mcp.ResolvedTool {
	return mcp.ResolvedTool{
		GrantedTool: model.GrantedTool{
			ServerName: serverName,
			ToolName:   toolName,
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
		LLMClient:    testutil.NewNoopLLMClient(),
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
	}{
		{
			name:      "empty_tools_produces_empty_slice",
			tools:     nil,
			wantCount: 0,
		},
		{
			name: "single_tool_original_name_and_description",
			tools: []mcp.ResolvedTool{
				{
					GrantedTool: model.GrantedTool{
						ServerName: "my-server",
						ToolName:   "read.data",
					},
					Description: "reads some data",
					InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
				},
			},
			wantCount: 1,
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
				LLMClient:    testutil.NewNoopLLMClient(),
				Audit:        NewAuditWriter(s.Queries()),
				StateMachine: NewRunStateMachine("run1", model.RunStatusRunning, s.Queries()),
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			defs := ba.buildToolDefinitions()
			if len(defs) != tc.wantCount {
				t.Errorf("len(defs) = %d, want %d", len(defs), tc.wantCount)
			}

			// For the single-tool case, verify the agent returns the original
			// dot-separated name (sanitization is the LLMClient's responsibility).
			if tc.name == "single_tool_original_name_and_description" && len(defs) == 1 {
				def := defs[0]
				wantName := "my-server.read.data"
				if def.Name != wantName {
					t.Errorf("tool name = %q, want %q", def.Name, wantName)
				}
				if def.Description != "reads some data" {
					t.Errorf("tool description = %q, want %q", def.Description, "reads some data")
				}
			}
		})
	}
}

func TestWaitForApproval(t *testing.T) {
	t.Run("Timeout_Reject", func(t *testing.T) {
		approvalCh := make(chan bool) // unbuffered — timeout fires before any send
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

	t.Run("Timeout_AlwaysRejects", func(t *testing.T) {
		// Verify that timeout always results in an error regardless of OnTimeout value,
		// since on_timeout: approve was removed (issue #313).
		approvalCh := make(chan bool) // unbuffered — timeout fires before any send
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
			t.Error("expected error on timeout, got nil")
		}
		if !strings.Contains(err.Error(), "approval timeout") {
			t.Errorf("error message = %q, want to contain 'approval timeout'", err.Error())
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

func TestWaitForApproval_DBAndSSE(t *testing.T) {
	t.Run("creates_approval_request_record_and_publishes_event", func(t *testing.T) {
		s := testutil.NewTestStore(t)
		testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
		testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

		pub := &capturePublisher{}
		approvalCh := make(chan bool, 1)
		approvalCh <- true // approved immediately

		w := NewAuditWriter(s.Queries())
		ba, err := New(Config{
			Policy:       minimalPolicy(),
			Tools:        nil,
			LLMClient:    testutil.NewNoopLLMClient(),
			Audit:        w,
			ApprovalCh:   (<-chan bool)(approvalCh),
			StateMachine: NewRunStateMachine("run1", model.RunStatusRunning, s.Queries(), WithStateMachinePublisher(pub)),
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		entry := resolvedToolEntry{
			tool: mcp.ResolvedTool{
				GrantedTool: model.GrantedTool{},
			},
		}

		if err := ba.waitForApproval(context.Background(), "run1", entry, "my-server.do_thing", map[string]any{}); err != nil {
			t.Fatalf("waitForApproval: %v", err)
		}

		// Verify approval_requests DB record was created. The record stays 'pending'
		// until the approval scanner updates it — waitForApproval only creates the
		// record; resolving it is the scanner's responsibility.
		pending, err := s.GetPendingApprovalRequestsByRun(context.Background(), "run1")
		if err != nil {
			t.Fatalf("GetPendingApprovalRequestsByRun: %v", err)
		}
		if len(pending) != 1 {
			t.Errorf("pending approval requests = %d, want 1", len(pending))
		}

		// Verify the SSE event was published.
		events := pub.all()
		var approvalEvent *capturedEvent
		for i := range events {
			if events[i].eventType == "approval.created" {
				approvalEvent = &events[i]
				break
			}
		}
		if approvalEvent == nil {
			t.Fatal("expected approval.created SSE event, none found")
		}
		var payload map[string]string
		if err := json.Unmarshal(approvalEvent.data, &payload); err != nil {
			t.Fatalf("unmarshal approval.created data: %v", err)
		}
		if payload["run_id"] != "run1" {
			t.Errorf("approval.created run_id = %q, want %q", payload["run_id"], "run1")
		}
		if payload["approval_id"] == "" {
			t.Error("approval.created approval_id is empty")
		}
	})

	t.Run("transitions_run_to_waiting_and_back_to_running_on_approval", func(t *testing.T) {
		s := testutil.NewTestStore(t)
		testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
		testutil.InsertRun(t, s, "run2", "p1", model.RunStatusRunning)

		approvalCh := make(chan bool, 1)
		approvalCh <- true

		w := NewAuditWriter(s.Queries())
		sm := NewRunStateMachine("run2", model.RunStatusRunning, s.Queries())
		ba, err := New(Config{
			Policy:       minimalPolicy(),
			Tools:        nil,
			LLMClient:    testutil.NewNoopLLMClient(),
			Audit:        w,
			ApprovalCh:   (<-chan bool)(approvalCh),
			StateMachine: sm,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		entry := resolvedToolEntry{
			tool: mcp.ResolvedTool{GrantedTool: model.GrantedTool{}},
		}

		if err := ba.waitForApproval(context.Background(), "run2", entry, "my-server.do_thing", map[string]any{}); err != nil {
			t.Fatalf("waitForApproval: %v", err)
		}

		// After approval the state machine should be back at running.
		if got := sm.Current(); got != model.RunStatusRunning {
			t.Errorf("run status after approval = %q, want %q", got, model.RunStatusRunning)
		}
	})
}

func TestRunAPILoop_EndTurn(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		Policy:       minimalPolicy(),
		Tools:        nil,
		LLMClient:    testutil.NewMockLLMClient(testutil.MakeLLMTextResponse("all done", llm.StopReasonEndTurn, 10, 5)),
		Audit:        w,
		StateMachine: NewRunStateMachine("r1", model.RunStatusRunning, s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	history := []llm.ConversationTurn{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock{Text: "go"}}},
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
		LLMClient:    testutil.NewNoopLLMClient(),
		Audit:        w,
		StateMachine: NewRunStateMachine("run1", model.RunStatusRunning, s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ba.logAuditError(context.Background(), "run1", "something went wrong", model.ErrorCode("test_code"))

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

// feedbackPolicy returns a ParsedPolicy with feedback enabled.
func feedbackPolicy() *model.ParsedPolicy {
	return &model.ParsedPolicy{
		Name: "test-policy",
		Agent: model.AgentConfig{
			Task: "test task",
		},
		Capabilities: model.CapabilitiesConfig{
			Feedback: model.FeedbackConfig{Enabled: true},
		},
	}
}

func TestHandleToolCall_AskOperator_Success(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	feedbackCh := make(chan string, 1)
	feedbackCh <- "operator says hello"

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		LLMClient: testutil.NewMockLLMClient(
			testutil.MakeLLMToolCallResponse("tc-1", AskOperatorToolName,
				map[string]any{"reason": "Need clarification", "context": "Some details"}, 10, 5),
			testutil.MakeLLMTextResponse("All done.", llm.StopReasonEndTurn, 5, 3),
		),
		Tools:        nil,
		Policy:       feedbackPolicy(),
		Audit:        w,
		FeedbackCh:   feedbackCh,
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

	// Verify run completed successfully.
	run, dbErr := s.GetRun(context.Background(), "r1")
	if dbErr != nil {
		t.Fatalf("GetRun: %v", dbErr)
	}
	if run.Status != string(model.RunStatusComplete) {
		t.Errorf("run status = %q, want %q", run.Status, model.RunStatusComplete)
	}

	// Verify audit trail: feedback_request and feedback_response must exist;
	// tool_call and tool_result must not exist for synthetic tool calls.
	var hasFeedbackRequest, hasFeedbackResponse, hasToolCall, hasToolResult bool
	var feedbackRequestContent map[string]any
	var feedbackResponseContent map[string]any

	for _, step := range steps {
		switch step.Type {
		case string(model.StepTypeFeedbackRequest):
			hasFeedbackRequest = true
			json.Unmarshal([]byte(step.Content), &feedbackRequestContent) //nolint:errcheck
		case string(model.StepTypeFeedbackResponse):
			hasFeedbackResponse = true
			json.Unmarshal([]byte(step.Content), &feedbackResponseContent) //nolint:errcheck
		case string(model.StepTypeToolCall):
			hasToolCall = true
		case string(model.StepTypeToolResult):
			hasToolResult = true
		}
	}

	if !hasFeedbackRequest {
		t.Error("expected feedback_request step in audit trail")
	}
	if !hasFeedbackResponse {
		t.Error("expected feedback_response step in audit trail")
	}
	if hasToolCall {
		t.Error("unexpected tool_call step for synthetic tool — synthetic tools bypass MCP")
	}
	if hasToolResult {
		t.Error("unexpected tool_result step for synthetic tool — synthetic tools bypass MCP")
	}

	// Verify feedback_request content contains the message built from reason+context.
	if feedbackRequestContent != nil {
		msg, _ := feedbackRequestContent["message"].(string)
		if !strings.Contains(msg, "Need clarification") {
			t.Errorf("feedback_request message %q does not contain 'Need clarification'", msg)
		}
		if !strings.Contains(msg, "Some details") {
			t.Errorf("feedback_request message %q does not contain 'Some details'", msg)
		}
	}

	// Verify feedback_response content contains the operator's response.
	if feedbackResponseContent != nil {
		resp, _ := feedbackResponseContent["response"].(string)
		if resp != "operator says hello" {
			t.Errorf("feedback_response response = %q, want %q", resp, "operator says hello")
		}
	}
}

func TestHandleToolCall_AskOperator_NotAvailableWhenDisabled(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	// Policy has feedback disabled — gleipnir.ask_operator must not be offered to the LLM.
	mockClient := testutil.NewMockLLMClient(
		// LLM hallucinates a call to gleipnir.ask_operator even though it was never registered.
		testutil.MakeLLMToolCallResponse("tc-1", AskOperatorToolName,
			map[string]any{"reason": "something"}, 10, 5),
	)

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		LLMClient:    mockClient,
		Tools:        nil,
		Policy:       minimalPolicy(), // feedback disabled
		Audit:        w,
		StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := ba.Run(context.Background(), "r1", "trigger")
	if runErr == nil {
		t.Fatal("expected error when feedback is disabled, got nil")
	}
	if !strings.Contains(runErr.Error(), "not enabled") {
		t.Errorf("error = %q, want to contain 'not enabled'", runErr.Error())
	}

	// Run must be marked failed.
	run, dbErr := s.GetRun(context.Background(), "r1")
	if dbErr != nil {
		t.Fatalf("GetRun: %v", dbErr)
	}
	if run.Status != string(model.RunStatusFailed) {
		t.Errorf("run status = %q, want %q", run.Status, model.RunStatusFailed)
	}

	// At least one error step must exist.
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
		t.Error("expected at least one error step in audit trail")
	}

	// Verify gleipnir.ask_operator was not in the tools sent to the LLM.
	requests := mockClient.Requests()
	if len(requests) == 0 {
		t.Fatal("expected at least one API request")
	}
	for _, tool := range requests[0].Tools {
		if tool.Name == AskOperatorToolName {
			t.Errorf("gleipnir.ask_operator must not be in tool list when feedback is disabled")
		}
	}
}

func TestHandleToolCall_AskOperator_ReasonRequired(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	feedbackCh := make(chan string, 1)

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		LLMClient: testutil.NewMockLLMClient(
			// Missing required "reason" field — only "context" provided.
			testutil.MakeLLMToolCallResponse("tc-1", AskOperatorToolName,
				map[string]any{"context": "only context, no reason"}, 10, 5),
		),
		Tools:        nil,
		Policy:       feedbackPolicy(),
		Audit:        w,
		FeedbackCh:   feedbackCh,
		StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := ba.Run(context.Background(), "r1", "trigger")
	if runErr == nil {
		t.Fatal("expected schema violation error, got nil")
	}

	// Run must be marked failed.
	run, dbErr := s.GetRun(context.Background(), "r1")
	if dbErr != nil {
		t.Fatalf("GetRun: %v", dbErr)
	}
	if run.Status != string(model.RunStatusFailed) {
		t.Errorf("run status = %q, want %q", run.Status, model.RunStatusFailed)
	}

	// An error step with schema_violation code must exist.
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

func TestBuildToolDefinitions_IncludesAskOperator(t *testing.T) {
	mcpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer mcpSrv.Close()

	mcpTool := mcp.ResolvedTool{
		GrantedTool: model.GrantedTool{
			ServerName: "my-server",
			ToolName:   "read_data",
		},
		Client:      mcp.NewClient(mcpSrv.URL),
		Description: "reads data",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}

	t.Run("feedback_enabled_includes_ask_operator", func(t *testing.T) {
		s := testutil.NewTestStore(t)
		testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
		testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)

		ba, err := New(Config{
			Policy:       feedbackPolicy(),
			Tools:        []mcp.ResolvedTool{mcpTool},
			LLMClient:    testutil.NewNoopLLMClient(),
			Audit:        NewAuditWriter(s.Queries()),
			StateMachine: NewRunStateMachine("r1", model.RunStatusRunning, s.Queries()),
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		defs := ba.buildToolDefinitions()
		if len(defs) != 2 {
			t.Fatalf("len(defs) = %d, want 2 (MCP tool + gleipnir.ask_operator)", len(defs))
		}
		var hasAskOperator bool
		for _, def := range defs {
			if def.Name == AskOperatorToolName {
				hasAskOperator = true
			}
		}
		if !hasAskOperator {
			t.Error("expected gleipnir.ask_operator in tool definitions when feedback is enabled")
		}
	})

	t.Run("feedback_disabled_excludes_ask_operator", func(t *testing.T) {
		s := testutil.NewTestStore(t)
		testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
		testutil.InsertRun(t, s, "r1", "p1", model.RunStatusRunning)

		ba, err := New(Config{
			Policy:       minimalPolicy(), // feedback disabled
			Tools:        []mcp.ResolvedTool{mcpTool},
			LLMClient:    testutil.NewNoopLLMClient(),
			Audit:        NewAuditWriter(s.Queries()),
			StateMachine: NewRunStateMachine("r1", model.RunStatusRunning, s.Queries()),
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		defs := ba.buildToolDefinitions()
		if len(defs) != 1 {
			t.Fatalf("len(defs) = %d, want 1 (only MCP tool)", len(defs))
		}
		for _, def := range defs {
			if def.Name == AskOperatorToolName {
				t.Error("gleipnir.ask_operator must not be in tool definitions when feedback is disabled")
			}
		}
	})
}

// TestRun_feedback_timeout verifies that when a feedback timeout is configured
// and the operator does not respond, the run fails with an ErrorCodeFeedbackTimeout
// error step and the run is marked failed.
func TestRun_feedback_timeout(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	// Policy has feedback enabled but no per-policy timeout.
	// The agent is configured with a short DefaultFeedbackTimeout so the test
	// completes quickly without waiting for a real 30-minute timeout.
	pol := &model.ParsedPolicy{
		Name: "test-policy",
		Agent: model.AgentConfig{
			Task: "test task",
		},
		Capabilities: model.CapabilitiesConfig{
			Feedback: model.FeedbackConfig{Enabled: true},
		},
	}

	// feedbackCh is never sent on — the operator does not respond (testing timeout path).
	feedbackCh := make(chan string) // unbuffered — nothing sends on this channel in this test

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		LLMClient: testutil.NewMockLLMClient(
			testutil.MakeLLMToolCallResponse("tc-1", AskOperatorToolName,
				map[string]any{"reason": "Need your input"}, 10, 5),
		),
		Tools:                  nil,
		Policy:                 pol,
		Audit:                  w,
		FeedbackCh:             feedbackCh,
		StateMachine:           NewRunStateMachine("r1", model.RunStatusPending, s.Queries()),
		DefaultFeedbackTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := ba.Run(context.Background(), "r1", "trigger")
	if runErr == nil {
		t.Fatal("expected error on feedback timeout, got nil")
	}
	if !strings.Contains(runErr.Error(), "feedback timeout") {
		t.Errorf("error = %q, want to contain 'feedback timeout'", runErr.Error())
	}

	// Run must be marked failed.
	run, dbErr := s.GetRun(context.Background(), "r1")
	if dbErr != nil {
		t.Fatalf("GetRun: %v", dbErr)
	}
	if run.Status != string(model.RunStatusFailed) {
		t.Errorf("run status = %q, want %q", run.Status, model.RunStatusFailed)
	}

	// An error step with feedback_timeout code must exist.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	steps, err := s.ListRunSteps(context.Background(), "r1")
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	var timeoutErrFound bool
	for _, step := range steps {
		if step.Type == string(model.StepTypeError) {
			var content map[string]string
			if err := json.Unmarshal([]byte(step.Content), &content); err == nil {
				if content["code"] == string(model.ErrorCodeFeedbackTimeout) {
					timeoutErrFound = true
				}
			}
		}
	}
	if !timeoutErrFound {
		t.Errorf("expected error step with code %q, not found in steps", model.ErrorCodeFeedbackTimeout)
	}
}

func TestCapabilitySnapshot_IncludesAskOperator(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		LLMClient:    testutil.NewMockLLMClient(testutil.MakeLLMTextResponse("done", llm.StopReasonEndTurn, 5, 3)),
		Tools:        nil,
		Policy:       feedbackPolicy(),
		Audit:        w,
		FeedbackCh:   make(chan string), // unbuffered — test verifies run completes without feedback
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

	// The first step must be the capability snapshot.
	if steps[0].Type != string(model.StepTypeCapabilitySnapshot) {
		t.Fatalf("first step type = %q, want %q", steps[0].Type, model.StepTypeCapabilitySnapshot)
	}

	// The snapshot tools list must include gleipnir.ask_operator.
	type snapshotContent struct {
		Tools []model.GrantedTool `json:"tools"`
	}
	var snap snapshotContent
	if err := json.Unmarshal([]byte(steps[0].Content), &snap); err != nil {
		t.Fatalf("unmarshal snapshot content: %v", err)
	}
	var found bool
	for _, gt := range snap.Tools {
		if gt.ServerName == "gleipnir" && gt.ToolName == "ask_operator" {
			found = true
		}
	}
	if !found {
		t.Errorf("gleipnir.ask_operator not found in capability snapshot tools: %v", snap.Tools)
	}
}

// makeAgentWithFeedback builds a BoundAgent with feedback enabled and a fresh test store.
// The run starts in RunStatusRunning (same as makeAgentWithTools).
func makeAgentWithFeedback(t *testing.T, feedbackCh chan string, feedbackTimeout time.Duration) (*BoundAgent, *db.Store, *AuditWriter) {
	t.Helper()
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		Policy:                 feedbackPolicy(),
		Tools:                  nil,
		LLMClient:              testutil.NewNoopLLMClient(),
		Audit:                  w,
		FeedbackCh:             (<-chan string)(feedbackCh),
		DefaultFeedbackTimeout: feedbackTimeout,
		StateMachine:           NewRunStateMachine("run1", model.RunStatusRunning, s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ba, s, w
}

// pollForApprovalRow polls the DB until an approval request exists for run1,
// or the deadline is exceeded. It returns the approval row ID.
// Synchronous polling avoids time.Sleep for the common case (row appears quickly).
func pollForApprovalRow(t *testing.T, s *db.Store, deadline time.Time) string {
	t.Helper()
	for time.Now().Before(deadline) {
		rows, err := s.GetPendingApprovalRequestsByRun(context.Background(), "run1")
		if err != nil {
			t.Fatalf("GetPendingApprovalRequestsByRun: %v", err)
		}
		if len(rows) > 0 {
			return rows[0].ID
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for approval row to appear in DB")
	return ""
}

// pollForFeedbackRow polls the DB until a feedback request exists for run1.
func pollForFeedbackRow(t *testing.T, s *db.Store, deadline time.Time) string {
	t.Helper()
	for time.Now().Before(deadline) {
		rows, err := s.GetPendingFeedbackRequestsByRun(context.Background(), "run1")
		if err != nil {
			t.Fatalf("GetPendingFeedbackRequestsByRun: %v", err)
		}
		if len(rows) > 0 {
			return rows[0].ID
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for feedback row to appear in DB")
	return ""
}

// countErrorStepsForRun returns the number of error-type run_steps for a run.
func countErrorStepsForRun(t *testing.T, s *db.Store, runID string) int {
	t.Helper()
	n, err := s.CountRunSteps(context.Background(), runID)
	if err != nil {
		t.Fatalf("CountRunSteps %s: %v", runID, err)
	}
	// CountRunSteps returns all steps; we need only error steps.
	steps, err := s.ListRunSteps(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListRunSteps %s: %v", runID, err)
	}
	_ = n
	count := 0
	for _, st := range steps {
		if st.Type == string(model.StepTypeError) {
			count++
		}
	}
	return count
}

// TestWaitForApproval_BufferedLateRejection verifies that a late rejection send
// (after the timeout has already returned) lands safely in the buffered channel
// without blocking, and that exactly one error step is written.
func TestWaitForApproval_BufferedLateRejection(t *testing.T) {
	approvalCh := make(chan bool, 1) // buffered cap-1, owned by this test
	ba, s, w := makeAgentWithTools(t, nil, approvalCh)
	defer w.Close()

	entry := resolvedToolEntry{
		tool: mcp.ResolvedTool{
			GrantedTool: model.GrantedTool{
				Timeout:   50 * time.Millisecond,
				OnTimeout: model.OnTimeoutReject,
			},
		},
	}

	// waitForApproval times out and returns an error.
	err := ba.waitForApproval(context.Background(), "run1", entry, "my-server.do_thing", map[string]any{})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}

	// Now send a late rejection on the buffered channel. Because the channel has
	// capacity 1 and the receiver has already exited, the send must land in the
	// buffer (not hit the default arm). This proves the buffer absorbs late sends
	// without goroutine leaks or panics.
	sentToBuffer := false
	select {
	case approvalCh <- false:
		sentToBuffer = true
	default:
	}
	if !sentToBuffer {
		t.Error("late rejection send hit default arm; expected it to land in the cap-1 buffer")
	}

	// Flush the audit writer and assert exactly one error step.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := countErrorStepsForRun(t, s, "run1"); n != 1 {
		t.Errorf("error steps = %d, want 1", n)
	}
}

// TestWaitForApproval_ScannerWins verifies that when the background scanner
// resolves an approval request before the in-agent timer fires, the agent's
// timeout branch detects rows==0 and returns a sentinel error without writing a
// duplicate error step.
//
// The scanner is driven synchronously (no Start/ticker) to avoid wall-clock races
// under -race. The sequence is:
//  1. waitForApproval starts in a goroutine (Timeout=200ms).
//  2. The main goroutine polls until the approval row appears, then back-dates it.
//  3. scanner.scan() is called synchronously — it wins the guarded UPDATE (rows=1),
//     writes the error step, and transitions the run to failed.
//  4. The agent's 200ms timer fires; its guarded UPDATE gets rows=0; it returns
//     a sentinel error without writing a second error step.
func TestWaitForApproval_ScannerWins(t *testing.T) {
	approvalCh := make(chan bool, 1)
	ba, s, w := makeAgentWithTools(t, nil, approvalCh)
	// Use a publisher so we can count run.status_changed events.
	pub := &capturePublisher{}
	ba.sm = NewRunStateMachine("run1", model.RunStatusRunning, s.Queries(), WithStateMachinePublisher(pub))

	entry := resolvedToolEntry{
		tool: mcp.ResolvedTool{
			GrantedTool: model.GrantedTool{
				Timeout:   200 * time.Millisecond,
				OnTimeout: model.OnTimeoutReject,
			},
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- ba.waitForApproval(context.Background(), "run1", entry, "my-server.do_thing", map[string]any{})
	}()

	// Poll until the approval row appears in the DB (waitForApproval creates it
	// inside sm.Transition). Bounded at 100ms to keep the test fast.
	approvalID := pollForApprovalRow(t, s, time.Now().Add(100*time.Millisecond))

	// Back-date the approval so the scanner picks it up as expired.
	past := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := s.DB().Exec(
		`UPDATE approval_requests SET expires_at = ? WHERE id = ?`, past, approvalID,
	); err != nil {
		t.Fatalf("back-date approval: %v", err)
	}

	// Drive the scanner synchronously. It wins the guarded UPDATE (rows=1).
	sc := approval.NewScanner(s, time.Minute, approval.WithPublisher(pub))
	if err := sc.Scan(context.Background()); err != nil {
		t.Fatalf("scanner.Scan: %v", err)
	}

	// Wait for waitForApproval to return (the 200ms timer fires).
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("waitForApproval expected error, got nil")
		}
		// The sentinel error does not contain "approval timeout" as a standalone
		// phrase — it indicates the scanner won the race.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("waitForApproval did not return within 500ms")
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Exactly one error step: written by the scanner, not by the agent.
	if n := countErrorStepsForRun(t, s, "run1"); n != 1 {
		t.Errorf("error steps = %d, want exactly 1 (scanner wins)", n)
	}

	// Approval row must be timeout.
	var approvalStatus string
	if err := s.DB().QueryRow(`SELECT status FROM approval_requests WHERE id = ?`, approvalID).Scan(&approvalStatus); err != nil {
		t.Fatalf("query approval: %v", err)
	}
	if approvalStatus != "timeout" {
		t.Errorf("approval status = %q, want timeout", approvalStatus)
	}

	// Run must be failed.
	var runStatus string
	if err := s.DB().QueryRow(`SELECT status FROM runs WHERE id = 'run1'`).Scan(&runStatus); err != nil {
		t.Fatalf("query run: %v", err)
	}
	if runStatus != "failed" {
		t.Errorf("run status = %q, want failed", runStatus)
	}

	// Exactly one run.status_changed event with status="failed" (from the scanner).
	// The SM also emits one for the waiting_for_approval transition, so total >= 2.
	if n := pub.countByStatus("run.status_changed", "failed"); n != 1 {
		t.Errorf("run.status_changed(failed) events = %d, want 1", n)
	}
}

// TestWaitForFeedback_BufferedLateResponse mirrors TestWaitForApproval_BufferedLateRejection:
// after feedback timeout fires and the function returns, a late response send lands
// safely in the buffered channel without blocking.
func TestWaitForFeedback_BufferedLateResponse(t *testing.T) {
	feedbackCh := make(chan string, 1)
	ba, s, w := makeAgentWithFeedback(t, feedbackCh, 50*time.Millisecond)
	defer w.Close()

	_, err := ba.waitForFeedback(context.Background(), "run1", "ask_operator", "{}", "please answer", 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}

	// Late response must land in the cap-1 buffer after the receiver exits.
	sentToBuffer := false
	select {
	case feedbackCh <- "late response":
		sentToBuffer = true
	default:
	}
	if !sentToBuffer {
		t.Error("late response send hit default arm; expected it to land in the cap-1 buffer")
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := countErrorStepsForRun(t, s, "run1"); n != 1 {
		t.Errorf("error steps = %d, want 1", n)
	}
}

// TestWaitForFeedback_ScannerWins mirrors TestWaitForApproval_ScannerWins for the
// feedback path: the scanner resolves the feedback row before the in-agent timer
// fires, leaving exactly one error step.
func TestWaitForFeedback_ScannerWins(t *testing.T) {
	feedbackCh := make(chan string, 1)
	ba, s, w := makeAgentWithFeedback(t, feedbackCh, 200*time.Millisecond)
	pub := &capturePublisher{}
	ba.sm = NewRunStateMachine("run1", model.RunStatusRunning, s.Queries(), WithStateMachinePublisher(pub))

	done := make(chan error, 1)
	go func() {
		_, err := ba.waitForFeedback(context.Background(), "run1", "ask_operator", "{}", "please answer", 200*time.Millisecond)
		done <- err
	}()

	// Poll until the feedback row appears in the DB.
	feedbackID := pollForFeedbackRow(t, s, time.Now().Add(100*time.Millisecond))

	// Back-date the feedback row so the scanner picks it up as expired.
	past := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := s.DB().Exec(
		`UPDATE feedback_requests SET expires_at = ? WHERE id = ?`, past, feedbackID,
	); err != nil {
		t.Fatalf("back-date feedback: %v", err)
	}

	// Drive the scanner synchronously. It wins the guarded UPDATE (rows=1).
	sc := feedbackscanner.NewScanner(s, time.Minute, feedbackscanner.WithPublisher(pub))
	if err := sc.Scan(context.Background()); err != nil {
		t.Fatalf("scanner.Scan: %v", err)
	}

	// Wait for waitForFeedback to return (the 200ms timer fires).
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("waitForFeedback expected error, got nil")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("waitForFeedback did not return within 500ms")
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Exactly one error step: written by the scanner.
	if n := countErrorStepsForRun(t, s, "run1"); n != 1 {
		t.Errorf("error steps = %d, want exactly 1 (scanner wins)", n)
	}

	// Feedback row must be timed_out.
	var feedbackStatus string
	if err := s.DB().QueryRow(`SELECT status FROM feedback_requests WHERE id = ?`, feedbackID).Scan(&feedbackStatus); err != nil {
		t.Fatalf("query feedback: %v", err)
	}
	if feedbackStatus != "timed_out" {
		t.Errorf("feedback status = %q, want timed_out", feedbackStatus)
	}

	// Run must be failed.
	var runStatus string
	if err := s.DB().QueryRow(`SELECT status FROM runs WHERE id = 'run1'`).Scan(&runStatus); err != nil {
		t.Fatalf("query run: %v", err)
	}
	if runStatus != "failed" {
		t.Errorf("run status = %q, want failed", runStatus)
	}

	// Exactly one run.status_changed event with status="failed" (from the scanner).
	// The SM also emits one for the waiting_for_feedback transition, so total >= 2.
	if n := pub.countByStatus("run.status_changed", "failed"); n != 1 {
		t.Errorf("run.status_changed(failed) events = %d, want 1", n)
	}
}

// TestWaitForFeedback_ResponseWins verifies the happy path: a response arrives
// before the timer fires, and subsequent scanner.scan() is a no-op (the row is
// no longer pending).
func TestWaitForFeedback_ResponseWins(t *testing.T) {
	feedbackCh := make(chan string, 1)
	feedbackCh <- "operator's answer" // pre-fill so the channel is ready immediately
	ba, s, w := makeAgentWithFeedback(t, feedbackCh, 200*time.Millisecond)
	defer w.Close()

	response, err := ba.waitForFeedback(context.Background(), "run1", "ask_operator", "{}", "please answer", 200*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForFeedback: %v", err)
	}
	if response != "operator's answer" {
		t.Errorf("response = %q, want %q", response, "operator's answer")
	}

	// Run must be back to running (sm transitioned back).
	var runStatus string
	if err := s.DB().QueryRow(`SELECT status FROM runs WHERE id = 'run1'`).Scan(&runStatus); err != nil {
		t.Fatalf("query run: %v", err)
	}
	if runStatus != "running" {
		t.Errorf("run status = %q, want running (response arrived before timeout)", runStatus)
	}

	// The feedback row is still 'pending' (the handler updates it, not waitForFeedback).
	// Verify the scanner does nothing when it runs — no error steps, no events.
	pub := &capturePublisher{}
	sc := feedbackscanner.NewScanner(s, time.Minute, feedbackscanner.WithPublisher(pub))
	if err := sc.Scan(context.Background()); err != nil {
		t.Fatalf("scanner.Scan after response: %v", err)
	}

	// The scanner must not have published events — the row was resolved by the
	// operator response before it expired, so it should not be expired now.
	if n := pub.countByType("run.status_changed"); n != 0 {
		t.Errorf("run.status_changed events after scan = %d, want 0", n)
	}
}

// TestRun_ThinkingBlocksIncludedInHistory verifies that ThinkingBlocks returned
// by the LLM are prepended into the assistant turn history passed to the next
// API call. This enables multi-turn thinking continuity for providers that
// require it (e.g. Anthropic via Signature).
func TestRun_ThinkingBlocksIncludedInHistory(t *testing.T) {
	mcpSrv := makeToolCallServer(t, json.RawMessage(`[{"type":"text","text":"result"}]`), false)
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	tools := []mcp.ResolvedTool{toolForRun(mcpSrv.URL, "my-server", "read_data")}

	// First response: tool call with a ThinkingBlock carrying a Signature.
	firstResp := &llm.MessageResponse{
		Thinking:   []llm.ThinkingBlock{{Text: "I need to call the tool", Signature: "sig_turn1"}},
		ToolCalls:  []llm.ToolCallBlock{{ID: "tc-1", Name: "my-server.read_data", Input: json.RawMessage(`{}`)}},
		StopReason: llm.StopReasonToolUse,
		Usage:      llm.TokenUsage{InputTokens: 10, OutputTokens: 5},
	}

	mock := testutil.NewMockLLMClient(
		firstResp,
		testutil.MakeLLMTextResponse("Done.", llm.StopReasonEndTurn, 5, 3),
	)

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		LLMClient:    mock,
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

	if mock.Calls() != 2 {
		t.Fatalf("expected 2 API calls, got %d", mock.Calls())
	}

	// The second request's history should include the assistant turn from the
	// first response. That turn must contain the ThinkingBlock before the tool call.
	reqs := mock.Requests()
	secondReq := reqs[1]

	// Find the assistant turn in history.
	var assistantTurn *llm.ConversationTurn
	for i := range secondReq.History {
		if secondReq.History[i].Role == llm.RoleAssistant {
			assistantTurn = &secondReq.History[i]
			break
		}
	}
	if assistantTurn == nil {
		t.Fatal("expected assistant turn in second request's history")
	}

	// The first block must be the ThinkingBlock.
	if len(assistantTurn.Content) < 2 {
		t.Fatalf("assistant turn content len = %d, want >= 2", len(assistantTurn.Content))
	}
	tb, ok := assistantTurn.Content[0].(llm.ThinkingBlock)
	if !ok {
		t.Fatalf("first content block type = %T, want llm.ThinkingBlock", assistantTurn.Content[0])
	}
	if tb.Signature != "sig_turn1" {
		t.Errorf("ThinkingBlock.Signature = %q, want sig_turn1", tb.Signature)
	}
	if tb.Text != "I need to call the tool" {
		t.Errorf("ThinkingBlock.Text = %q, want 'I need to call the tool'", tb.Text)
	}

	// The second block must be the ToolCallBlock.
	if _, ok := assistantTurn.Content[1].(llm.ToolCallBlock); !ok {
		t.Fatalf("second content block type = %T, want llm.ToolCallBlock", assistantTurn.Content[1])
	}
}
