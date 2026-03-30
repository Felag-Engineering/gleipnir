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

	"github.com/rapp992/gleipnir/internal/db"
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
			Role:       model.CapabilityRoleTool,
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
			Role:       model.CapabilityRoleTool,
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
						Role:       model.CapabilityRoleTool,
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

	t.Run("Timeout_AlwaysRejects", func(t *testing.T) {
		// Verify that timeout always results in an error regardless of OnTimeout value,
		// since on_timeout: approve was removed (issue #313).
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
