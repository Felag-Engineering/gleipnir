// Package agent — headerparam_test.go pins down agent.go's dispatch of a
// #747 *mcp.HeaderParamError from CallTool into the same structural-error
// shape #744's ArgValidator violations use (schemaViolation), and locks the
// one place that shape diverges from its ArgValidator sibling
// (argvalidate_test.go's TestHandleToolCall_CanonicalSchemaValidation): a
// HeaderParamError is raised from inside entry.tool.Client.CallTool, which
// is dispatched AFTER the tool_call audit step is already written — so,
// unlike an ArgValidator violation, the run DOES have a tool_call step for
// the rejected call.
package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

func TestHandleToolCall_HeaderParamRejection_IsCorrectable(t *testing.T) {
	srv, callCount := newToolCallCountingServer(t, json.RawMessage(`[{"type":"text","text":"ok"}]`), false)

	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	// The annotation names a reserved header (Mcp-Name), so extraction fails
	// closed on the declaration itself before any HTTP request is made.
	canonicalSchema := json.RawMessage(`{"type":"object","properties":{"trace_id":{"type":"string","x-mcp-header":"Mcp-Name"}}}`)
	tool := mcp.ResolvedTool{
		GrantedTool: model.GrantedTool{
			ServerName: "my-server",
			ToolName:   "do_thing",
			Approval:   model.ApprovalModeNone,
		},
		Client:          mcp.NewClient(srv.URL, mcp.WithProtocolVersion(mcp.ProtocolVersion20260728)),
		Description:     "a test tool",
		InputSchema:     canonicalSchema,
		CanonicalSchema: canonicalSchema,
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

	output, isErr, callErr := ba.handleToolCall(context.Background(), "run1", "my-server.do_thing", map[string]any{"trace_id": "abc-123"})
	if callErr != nil {
		t.Fatalf("handleToolCall returned unexpected error: %v", callErr)
	}
	if !isErr {
		t.Errorf("isErr = false, want true; output = %q", output)
	}
	if *callCount != 0 {
		t.Errorf("MCP server tools/call count = %d, want 0 (rejection must be pre-dispatch)", *callCount)
	}
	if code, found := findErrorStepCode(t, s, "run1"); !found || code != string(model.ErrorCodeSchemaViolation) {
		t.Errorf("error step code = %q (found=%v), want %q", code, found, model.ErrorCodeSchemaViolation)
	}
	if !hasErrorToolResultStep(t, s, "run1") {
		t.Error("expected a tool_result step with is_error: true")
	}
	// The divergence from the ArgValidator case: CallTool is dispatched
	// after the tool_call step is written (agent.go), so a rejected
	// x-mcp-header declaration's run DOES have a tool_call step.
	if !hasStepType(t, s, "run1", model.StepTypeToolCall) {
		t.Error("tool_call step must be present — the rejection happens inside CallTool, after the step is written")
	}
}
