// Package agent — argvalidate_test.go pins down pre-dispatch exact argument
// enforcement (#744): handleToolCall's two schema-validation gates (the
// ADR-017 key-presence gate, then the exact ArgValidator gate), the
// NULL/uncompilable canonical-schema fallback (decisions (c) and (d)), and
// that both gates still run strictly before approval gating (ADR-008).
package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// newToolCallCountingServer starts an httptest.Server that counts tools/call
// requests only (ignoring the MCP initialize handshake) and always responds
// with the given content/isError. Mirrors the counting convention used by
// TestRun_MCPTransportError_BecomesToolResult / TestRun_ToolCallCapExceeded.
func newToolCallCountingServer(t *testing.T, content json.RawMessage, isError bool) (*httptest.Server, *int) {
	t.Helper()
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err == nil {
			if method, _ := req["method"].(string); method == "tools/call" {
				count++
			}
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
	return srv, &count
}

// hasStepType reports whether run runID has at least one step of type st.
func hasStepType(t *testing.T, s *db.Store, runID string, st model.StepType) bool {
	t.Helper()
	steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: runID, After: -1, Limit: listAll})
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	for _, step := range steps {
		if step.Type == string(st) {
			return true
		}
	}
	return false
}

// findErrorStepCode returns the code of the first error step found for runID.
func findErrorStepCode(t *testing.T, s *db.Store, runID string) (code string, found bool) {
	t.Helper()
	steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: runID, After: -1, Limit: listAll})
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	for _, step := range steps {
		if step.Type != string(model.StepTypeError) {
			continue
		}
		var content model.ErrorStepContent
		if err := json.Unmarshal([]byte(step.Content), &content); err == nil {
			return string(content.Code), true
		}
	}
	return "", false
}

// hasErrorToolResultStep reports whether run runID has a tool_result step
// with is_error: true.
func hasErrorToolResultStep(t *testing.T, s *db.Store, runID string) bool {
	t.Helper()
	steps, err := s.ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: runID, After: -1, Limit: listAll})
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	for _, step := range steps {
		if step.Type != string(model.StepTypeToolResult) {
			continue
		}
		var content map[string]any
		if err := json.Unmarshal([]byte(step.Content), &content); err == nil {
			if isErr, _ := content["is_error"].(bool); isErr {
				return true
			}
		}
	}
	return false
}

// TestHandleToolCall_CanonicalSchemaValidation covers the exact ArgValidator
// gate directly: wrong type, missing required, a root-level oneOf branch
// violation (#769 evidence — see the oneOf case's comment), and a valid call.
func TestHandleToolCall_CanonicalSchemaValidation(t *testing.T) {
	tests := []struct {
		name            string
		canonicalSchema json.RawMessage
		input           map[string]any
		wantValid       bool
		wantFieldSubstr string // violation cases: output must mention this field name
	}{
		{
			name:            "wrong type",
			canonicalSchema: json.RawMessage(`{"type":"object","properties":{"arg":{"type":"string"}}}`),
			input:           map[string]any{"arg": float64(123)},
			wantFieldSubstr: "arg",
		},
		{
			name:            "missing required",
			canonicalSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
			input:           map[string]any{},
			wantFieldSubstr: "name",
		},
		{
			// Root-level oneOf has no top-level "properties", so the ADR-017
			// key-presence gate is a no-op for this tool (#769) — the
			// rejection below comes entirely from the exact gate. This is
			// the "narrows, does not close" evidence for #769: the branch's
			// own declared type is enforced even though the operator's
			// params narrowing would still be a silent no-op here.
			name: "oneOf branch violation",
			canonicalSchema: json.RawMessage(`{"oneOf":[
				{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},
				{"type":"object","properties":{"b":{"type":"string"}},"required":["b"]}
			]}`),
			input:           map[string]any{"a": float64(123)},
			wantFieldSubstr: "a",
		},
		{
			name:            "valid call",
			canonicalSchema: json.RawMessage(`{"type":"object","properties":{"arg":{"type":"string"}}}`),
			input:           map[string]any{"arg": "ok"},
			wantValid:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, callCount := newToolCallCountingServer(t, json.RawMessage(`[{"type":"text","text":"ok"}]`), false)

			s := testutil.NewTestStore(t)
			testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
			testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

			tool := mcp.ResolvedTool{
				GrantedTool: model.GrantedTool{
					ServerName: "my-server",
					ToolName:   "do_thing",
					Approval:   model.ApprovalModeNone,
				},
				Client:          mcp.NewClient(srv.URL),
				Description:     "a test tool",
				InputSchema:     tc.canonicalSchema,
				CanonicalSchema: tc.canonicalSchema,
			}

			w := NewAuditWriter(s.Queries())
			ba, err := New(Config{
				Policy:       minimalPolicy(),
				Tools:        []mcp.ResolvedTool{tool},
				Audit:        w,
				LLMClient:    testutil.NewNoopLLMClient(),
				ApprovalCh:   make(chan bool),
				StateMachine: NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries()),
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			output, isErr, callErr := ba.handleToolCall(context.Background(), "run1", "my-server.do_thing", tc.input)
			if callErr != nil {
				t.Fatalf("handleToolCall returned unexpected error: %v", callErr)
			}

			if tc.wantValid {
				if isErr {
					t.Errorf("isErr = true, want false; output = %q", output)
				}
				if *callCount != 1 {
					t.Errorf("MCP server tools/call count = %d, want 1", *callCount)
				}
				return
			}

			if !isErr {
				t.Errorf("isErr = false, want true; output = %q", output)
			}
			if !strings.Contains(output, tc.wantFieldSubstr) {
				t.Errorf("output %q does not mention field %q", output, tc.wantFieldSubstr)
			}
			if *callCount != 0 {
				t.Errorf("MCP server tools/call count = %d, want 0 (schema violation must block dispatch)", *callCount)
			}
			if code, found := findErrorStepCode(t, s, "run1"); !found || code != string(model.ErrorCodeSchemaViolation) {
				t.Errorf("error step code = %q (found=%v), want %q", code, found, model.ErrorCodeSchemaViolation)
			}
			if !hasErrorToolResultStep(t, s, "run1") {
				t.Error("expected a tool_result step with is_error: true")
			}
			if hasStepType(t, s, "run1", model.StepTypeToolCall) {
				t.Error("tool_call step must NOT be written when schema validation fails")
			}
		})
	}
}

// TestHandleToolCall_NullCanonicalSchema_FallsBackToKeyPresence locks
// decision (c): a NULL canonical schema disables exact enforcement but
// leaves the ADR-017 key-presence gate running exactly as it does today —
// including that gate's fatality: an undeclared key still fails the run.
func TestHandleToolCall_NullCanonicalSchema_FallsBackToKeyPresence(t *testing.T) {
	inputSchema := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`)

	tests := []struct {
		name         string
		input        map[string]any
		wantDispatch bool // true: reaches the MCP server (only key-presence ran)
		wantErr      bool // true: gate 1 rejects and the run fails
	}{
		{
			name:         "wrong type reaches the MCP server — no exact enforcement without a canonical schema",
			input:        map[string]any{"a": float64(123)},
			wantDispatch: true,
		},
		{
			name:    "undeclared key still fails the run — the ADR-017 gate still runs and is fatal",
			input:   map[string]any{"b": float64(1)},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, callCount := newToolCallCountingServer(t, json.RawMessage(`[{"type":"text","text":"ok"}]`), false)

			s := testutil.NewTestStore(t)
			testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
			testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

			tool := mcp.ResolvedTool{
				GrantedTool: model.GrantedTool{ServerName: "my-server", ToolName: "do_thing"},
				Client:      mcp.NewClient(srv.URL),
				Description: "a test tool",
				InputSchema: inputSchema,
				// CanonicalSchema intentionally left nil.
			}

			w := NewAuditWriter(s.Queries())
			ba, err := New(Config{
				Policy:       minimalPolicy(),
				Tools:        []mcp.ResolvedTool{tool},
				Audit:        w,
				LLMClient:    testutil.NewNoopLLMClient(),
				ApprovalCh:   make(chan bool),
				StateMachine: NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries()),
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			entry, ok := ba.toolsByName["my-server.do_thing"]
			if !ok || entry.argValidator != nil {
				t.Fatalf("argValidator = %v (ok=%v), want nil for a NULL canonical schema", entry.argValidator, ok)
			}

			_, isErr, callErr := ba.handleToolCall(context.Background(), "run1", "my-server.do_thing", tc.input)

			if tc.wantErr {
				if callErr == nil {
					t.Fatal("handleToolCall returned nil error; want a fatal ADR-017 key-presence violation")
				}
				if *callCount != 0 {
					t.Errorf("MCP server tools/call count = %d, want 0 (ADR-017 gate must still reject)", *callCount)
				}
				return
			}
			if callErr != nil {
				t.Fatalf("handleToolCall returned unexpected error: %v", callErr)
			}

			if tc.wantDispatch {
				if *callCount != 1 {
					t.Errorf("MCP server tools/call count = %d, want 1 (only key-presence should have run)", *callCount)
				}
				if isErr {
					t.Error("isErr = true, want false")
				}
				return
			}
		})
	}
}

// TestHandleToolCall_KeyPresenceGateStillRunsWithValidator locks decision
// (b2): the exact gate ADDS to the ADR-017 key-presence gate, it does not
// replace it. Without additionalProperties, the compiled schema alone would
// accept an extra key — only the key-presence gate (driven by the policy's
// params scoping) rejects it.
func TestHandleToolCall_KeyPresenceGateStillRunsWithValidator(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}}}`) // no additionalProperties

	// Sanity-check the test's premise: compiled alone (no ADR-017
	// narrowing), the schema tolerates "b".
	premise, err := mcp.NewArgValidator(schema, nil)
	if err != nil {
		t.Fatalf("mcp.NewArgValidator: %v", err)
	}
	if err := premise.Validate(map[string]any{"a": "x", "b": "y"}); err != nil {
		t.Fatalf("premise check failed: the unnarrowed schema alone rejected 'b': %v", err)
	}

	srv, callCount := newToolCallCountingServer(t, json.RawMessage(`[{"type":"text","text":"ok"}]`), false)

	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	tool := mcp.ResolvedTool{
		GrantedTool: model.GrantedTool{
			ServerName: "my-server",
			ToolName:   "do_thing",
			Params:     map[string]any{"a": "x"}, // ADR-017 scopes this tool to just "a"
		},
		Client:          mcp.NewClient(srv.URL),
		Description:     "a test tool",
		InputSchema:     schema,
		CanonicalSchema: schema,
	}

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		Policy:       minimalPolicy(),
		Tools:        []mcp.ResolvedTool{tool},
		Audit:        w,
		LLMClient:    testutil.NewNoopLLMClient(),
		ApprovalCh:   make(chan bool),
		StateMachine: NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	entry, ok := ba.toolsByName["my-server.do_thing"]
	if !ok || entry.argValidator == nil {
		t.Fatalf("argValidator = %v (ok=%v), want a compiled validator", entry.argValidator, ok)
	}

	_, isErr, callErr := ba.handleToolCall(context.Background(), "run1", "my-server.do_thing", map[string]any{"a": "x", "b": "y"})

	// Gate 1 (ADR-017 key-presence) is fatal, so 'b' being rejected surfaces
	// as a run-failing error here, not a correctable result — but it must
	// still be REJECTED, which is the property this test locks: without
	// gate 1 running unconditionally, this call would have reached the MCP
	// server (the compiled schema alone tolerates 'b', per the premise
	// check above).
	if callErr == nil {
		t.Fatal("handleToolCall returned nil error; want the ADR-017 key gate to reject 'b' even though the compiled schema alone would accept it")
	}
	if isErr {
		t.Error("isErr = true, want false — a gate-1 violation returns a fatal error, not a correctable result")
	}
	if *callCount != 0 {
		t.Errorf("MCP server tools/call count = %d, want 0", *callCount)
	}
}

// TestBuildResolvedToolMap_UncompilableCanonicalFallsBack locks decision
// (d): an uncompilable canonical schema does not fail New(), does not drop
// the tool, and leaves argValidator nil. Using a "$ref":"file://…" canonical
// schema also doubles as the "compile failure never reads a local file" gate
// — if the deny-all loader were missing, compilation might have succeeded
// (or failed differently) after reading /etc/passwd.
func TestBuildResolvedToolMap_UncompilableCanonicalFallsBack(t *testing.T) {
	srv, callCount := newToolCallCountingServer(t, json.RawMessage(`[{"type":"text","text":"ok"}]`), false)

	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	tool := mcp.ResolvedTool{
		GrantedTool:     model.GrantedTool{ServerName: "my-server", ToolName: "do_thing"},
		Client:          mcp.NewClient(srv.URL),
		Description:     "a test tool",
		InputSchema:     json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`),
		CanonicalSchema: json.RawMessage(`{"$ref":"file:///etc/passwd"}`),
	}

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		Policy:       minimalPolicy(),
		Tools:        []mcp.ResolvedTool{tool},
		Audit:        w,
		LLMClient:    testutil.NewNoopLLMClient(),
		ApprovalCh:   make(chan bool),
		StateMachine: NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	entry, ok := ba.toolsByName["my-server.do_thing"]
	if !ok {
		t.Fatal("tool entry not found — an uncompilable canonical schema must not drop the tool")
	}
	if entry.argValidator != nil {
		t.Error("argValidator should be nil — compile failure must fall back to key-presence")
	}

	// A wrong-typed call is not caught by any gate now (no exact enforcement,
	// and "a" is a declared key so ADR-017 passes) — it reaches the server.
	_, isErr, callErr := ba.handleToolCall(context.Background(), "run1", "my-server.do_thing", map[string]any{"a": float64(123)})
	if callErr != nil {
		t.Fatalf("handleToolCall returned unexpected error: %v", callErr)
	}
	if isErr {
		t.Error("isErr = true, want false — with no compiled validator, a wrong-typed call reaches the MCP server")
	}
	if *callCount != 1 {
		t.Errorf("MCP server tools/call count = %d, want 1", *callCount)
	}
}

// TestHandleToolCall_ValidationRunsBeforeApprovalGating is the Definition-of-
// Done ordering test: with approval: required AND invalid arguments, the
// call must be rejected by validation before it ever reaches the approval
// gate. ApprovalCh is an empty buffered channel — if the ordering
// regressed, ApprovalHandler.Wait would block forever on
// context.Background() (no deadline), so a regression here hangs the test
// rather than silently passing; the goroutine+select wraps that into an
// ordinary test failure instead of a full CI timeout.
func TestHandleToolCall_ValidationRunsBeforeApprovalGating(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	canonicalSchema := json.RawMessage(`{"type":"object","properties":{"arg":{"type":"string"}}}`)
	tool := mcp.ResolvedTool{
		GrantedTool: model.GrantedTool{
			ServerName: "my-server",
			ToolName:   "do_thing",
			Approval:   model.ApprovalModeRequired,
		},
		Client:          mcp.NewClient("http://unused.invalid"), // must never be dialed
		Description:     "a world-affecting tool",
		InputSchema:     canonicalSchema,
		CanonicalSchema: canonicalSchema,
	}

	approvalCh := make(chan bool, 1) // empty — see doc comment

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		Policy:       minimalPolicy(),
		Tools:        []mcp.ResolvedTool{tool},
		Audit:        w,
		LLMClient:    testutil.NewNoopLLMClient(),
		ApprovalCh:   approvalCh,
		StateMachine: NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	type result struct {
		output string
		isErr  bool
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, isErr, callErr := ba.handleToolCall(context.Background(), "run1", "my-server.do_thing", map[string]any{"arg": float64(123)})
		done <- result{output, isErr, callErr}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("handleToolCall returned unexpected error: %v", r.err)
		}
		if !r.isErr {
			t.Error("isErr = false, want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handleToolCall did not return within 2s — validation must run before approval gating")
	}

	if hasStepType(t, s, "run1", model.StepTypeApprovalRequest) {
		t.Error("approval_request step must NOT be written — validation should reject the call first")
	}
	// Belt-and-suspenders: the channel was never drained (nothing was ever
	// sent to it either, so this is consistent with Wait never running).
	if len(approvalCh) != 0 {
		t.Error("approval channel must be untouched")
	}
}

// TestRun_SchemaViolationIsCorrectable is the full-Run proof of decision (f):
// a schema violation no longer fails the run. The agent gets a second turn,
// sees the IsError tool result naming the bad field, and the run completes.
func TestRun_SchemaViolationIsCorrectable(t *testing.T) {
	srv, callCount := newToolCallCountingServer(t, json.RawMessage(`[{"type":"text","text":"ok"}]`), false)

	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "r1", "p1", model.RunStatusPending)

	canonicalSchema := json.RawMessage(`{"type":"object","properties":{"arg":{"type":"string"}}}`)
	tool := mcp.ResolvedTool{
		GrantedTool:     model.GrantedTool{ServerName: "my-server", ToolName: "read_data"},
		Client:          mcp.NewClient(srv.URL),
		Description:     "a test tool",
		InputSchema:     canonicalSchema,
		CanonicalSchema: canonicalSchema,
	}

	badToolCall := testutil.MakeLLMToolCallResponse("tu-1", "my-server.read_data", map[string]any{"arg": 123}, 10, 5)
	llmClient := testutil.NewMockLLMClient(badToolCall, testutil.MakeTextResponse("done"))

	w := NewAuditWriter(s.Queries())
	ba, err := New(Config{
		LLMClient:    llmClient,
		Tools:        []mcp.ResolvedTool{tool},
		Policy:       minimalPolicy(),
		Audit:        w,
		StateMachine: NewRunStateMachine("r1", model.RunStatusPending, s.DB(), s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	runErr := ba.Run(context.Background(), "r1", "trigger")
	if runErr != nil {
		t.Fatalf("Run returned error, want nil (schema violations must be correctable): %v", runErr)
	}

	run, err := s.GetRun(context.Background(), "r1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != string(model.RunStatusComplete) {
		t.Errorf("run status = %q, want %q", run.Status, model.RunStatusComplete)
	}

	if *callCount != 0 {
		t.Errorf("MCP server tools/call count = %d, want 0 (schema violation must block dispatch)", *callCount)
	}

	if code, found := findErrorStepCode(t, s, "r1"); !found || code != string(model.ErrorCodeSchemaViolation) {
		t.Errorf("error step code = %q (found=%v), want %q", code, found, model.ErrorCodeSchemaViolation)
	}

	requests := llmClient.Requests()
	if len(requests) < 2 {
		t.Fatalf("LLM was called %d times, want at least 2 (the second turn must carry the correction)", len(requests))
	}

	var foundCorrection bool
	for _, turn := range requests[1].History {
		for _, block := range turn.Content {
			if trb, ok := block.(llm.ToolResultBlock); ok && trb.IsError && strings.Contains(trb.Content, "arg") {
				foundCorrection = true
			}
		}
	}
	if !foundCorrection {
		t.Error("expected the second LLM request's history to contain an IsError ToolResultBlock mentioning 'arg'")
	}
}
