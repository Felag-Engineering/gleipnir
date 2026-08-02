package agent

import (
	"encoding/json"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// scrambledOrderTools returns 5 MCP tools whose construction order is
// deliberately NOT their sorted dot-name order, so a test asserting sorted
// output cannot pass by accident (e.g. if an implementation happened to
// preserve construction/grant order instead of actually sorting). Sorted
// dot-name order is: server-a.tool-2, server-b.tool-3, server-c.tool-1,
// server-d.tool-5, server-e.tool-4.
func scrambledOrderTools() []mcp.ResolvedTool {
	build := func(serverName, toolName string) mcp.ResolvedTool {
		return mcp.ResolvedTool{
			GrantedTool: model.GrantedTool{ServerName: serverName, ToolName: toolName},
			Client:      mcp.NewClient("http://example.invalid"),
			Description: serverName + "." + toolName + " description",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		}
	}
	return []mcp.ResolvedTool{
		build("server-c", "tool-1"),
		build("server-a", "tool-2"),
		build("server-b", "tool-3"),
		build("server-e", "tool-4"),
		build("server-d", "tool-5"),
	}
}

// sortedOrderNames is the dot-name order scrambledOrderTools' tools must be
// emitted in.
var sortedOrderNames = []string{
	"server-a.tool-2",
	"server-b.tool-3",
	"server-c.tool-1",
	"server-d.tool-5",
	"server-e.tool-4",
}

func namesOf(defs []llm.ToolDefinition) []string {
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	return names
}

// TestBuildToolDefinitions_OrderIsSortedAndStable verifies buildToolDefinitions
// emits tools sorted by dot-name, and that the order is identical on every
// call: a.toolsByName is a Go map, whose iteration order is randomized per
// run, so a test that calls buildToolDefinitions only once could pass by
// chance even without sorting — repeating the call is what makes this a real
// regression guard.
func TestBuildToolDefinitions_OrderIsSortedAndStable(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	ba, err := New(Config{
		Policy:       minimalPolicy(),
		Tools:        scrambledOrderTools(),
		LLMClient:    testutil.NewNoopLLMClient(),
		Audit:        NewAuditWriter(s.Queries()),
		StateMachine: NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for i := 0; i < 20; i++ {
		defs := ba.buildToolDefinitions()
		if len(defs) != len(sortedOrderNames) {
			t.Fatalf("run %d: len(defs) = %d, want %d", i, len(defs), len(sortedOrderNames))
		}
		for j, def := range defs {
			if def.Name != sortedOrderNames[j] {
				t.Fatalf("run %d: defs[%d].Name = %q, want %q (full order: %v)", i, j, def.Name, sortedOrderNames[j], namesOf(defs))
			}
		}
	}
}

// TestBuildToolDefinitions_AskOperatorAppendedLastAfterSortedTools verifies
// the synthetic gleipnir.ask_operator tool is appended after the sorted MCP
// tool block, not merged into the sort — it carries no dot-name in the
// <source>.<tool> namespace those entries come from.
func TestBuildToolDefinitions_AskOperatorAppendedLastAfterSortedTools(t *testing.T) {
	s := testutil.NewTestStore(t)
	testutil.InsertPolicy(t, s, "p1", "policy-p1", "webhook", "{}")
	testutil.InsertRun(t, s, "run1", "p1", model.RunStatusRunning)

	ba, err := New(Config{
		Policy:       feedbackPolicy(),
		Tools:        scrambledOrderTools(),
		LLMClient:    testutil.NewNoopLLMClient(),
		Audit:        NewAuditWriter(s.Queries()),
		StateMachine: NewRunStateMachine("run1", model.RunStatusRunning, s.DB(), s.Queries()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	defs := ba.buildToolDefinitions()
	if len(defs) == 0 {
		t.Fatal("expected at least one tool definition")
	}

	last := defs[len(defs)-1]
	if last.Name != AskOperatorToolName {
		t.Errorf("defs[last].Name = %q, want %q", last.Name, AskOperatorToolName)
	}

	preceding := defs[:len(defs)-1]
	if len(preceding) != len(sortedOrderNames) {
		t.Fatalf("len(preceding) = %d, want %d (full order: %v)", len(preceding), len(sortedOrderNames), namesOf(defs))
	}
	for i, def := range preceding {
		if def.Name != sortedOrderNames[i] {
			t.Errorf("preceding[%d].Name = %q, want %q", i, def.Name, sortedOrderNames[i])
		}
	}
}
