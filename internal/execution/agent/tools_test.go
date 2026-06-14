package agent

// Tests for the helper functions extracted from the plugin-tool processing loop
// in New() and the snapshot assembly in Run(). The integration guard
// (TestCapabilitySnapshot_IncludesPluginSourceTools) lives in agent_test.go and
// exercises the full BoundAgent loop; these unit tests exercise the helpers in
// isolation so the sync invariant and snapshot ordering have a focused surface.

import (
	"fmt"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// TestBuildPluginToolEntries_DispatchAndSnapshotStayInSync asserts that the
// returned dispatch map and snapshot slice have identical dot-name coverage and
// that every snapshot Source matches the dispatch entry's Source.
func TestBuildPluginToolEntries_DispatchAndSnapshotStayInSync(t *testing.T) {
	pts := []PluginToolEntry{
		{InstanceName: "inst-a", ToolName: "read", Generation: 1,
			Description: "reads", Schema: map[string]any{"type": "object"}},
		{InstanceName: "inst-b", ToolName: "write", Generation: 3,
			Description: "writes", Schema: map[string]any{"type": "object"}},
	}

	entries, granted, err := buildPluginToolEntries(pts)
	if err != nil {
		t.Fatalf("buildPluginToolEntries: %v", err)
	}

	// Same count in both outputs.
	if len(entries) != len(pts) {
		t.Errorf("dispatch map len = %d, want %d", len(entries), len(pts))
	}
	if len(granted) != len(pts) {
		t.Errorf("snapshot slice len = %d, want %d", len(granted), len(pts))
	}

	// Verify dispatch entries and snapshot entries are in sync by dot-name and Source.
	for i, pt := range pts {
		dotName := pt.InstanceName + "." + pt.ToolName
		wantSource := fmt.Sprintf("plugin:%s@%d", pt.InstanceName, pt.Generation)

		entry, ok := entries[dotName]
		if !ok {
			t.Errorf("dispatch map missing key %q", dotName)
			continue
		}
		gotSource := sourceString(entry)
		if gotSource != wantSource {
			t.Errorf("dispatch entry %q Source = %q, want %q", dotName, gotSource, wantSource)
		}

		gt := granted[i]
		if gt.Source != wantSource {
			t.Errorf("snapshot entry %d Source = %q, want %q", i, gt.Source, wantSource)
		}
		if gt.ToolName != pt.ToolName {
			t.Errorf("snapshot entry %d ToolName = %q, want %q", i, gt.ToolName, pt.ToolName)
		}
	}
}

func TestBuildPluginToolEntries_EmptyInput(t *testing.T) {
	entries, granted, err := buildPluginToolEntries(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries len = %d, want 0", len(entries))
	}
	if len(granted) != 0 {
		t.Errorf("granted len = %d, want 0", len(granted))
	}
}

func TestBuildPluginToolEntries_SchemaAndParamsApplied(t *testing.T) {
	// Params narrows the schema: only the allowed property should be exposed.
	pt := PluginToolEntry{
		InstanceName: "inst",
		ToolName:     "do-thing",
		Generation:   2,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"allowed": map[string]any{"type": "string"},
				"denied":  map[string]any{"type": "string"},
			},
		},
		Params: map[string]any{
			"properties": map[string]any{
				"allowed": map[string]any{"type": "string"},
			},
		},
	}

	entries, _, err := buildPluginToolEntries([]PluginToolEntry{pt})
	if err != nil {
		t.Fatalf("buildPluginToolEntries: %v", err)
	}
	entry, ok := entries["inst.do-thing"]
	if !ok {
		t.Fatal("dispatch map missing 'inst.do-thing'")
	}
	if entry.narrowedSchema == nil {
		t.Error("narrowedSchema is nil; want narrowed JSON")
	}
}

func TestBuildCapabilitySnapshotTools_Ordering(t *testing.T) {
	// Verify MCP → synthetic ask_operator → plugin ordering.
	mcpTools := []mcp.ResolvedTool{
		{
			GrantedTool: model.GrantedTool{ServerName: "srv", ToolName: "read"},
			Description: "reads",
		},
	}
	pluginGranted := []model.GrantedTool{
		{ToolName: "do-thing", Source: "plugin:inst@1"},
	}

	got := buildCapabilitySnapshotTools(mcpTools, pluginGranted, true)

	// Expected order: mcp-tool, ask_operator, plugin-tool.
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].ToolName != "read" {
		t.Errorf("got[0].ToolName = %q, want \"read\"", got[0].ToolName)
	}
	if got[0].Source != "mcp:srv" {
		t.Errorf("got[0].Source = %q, want \"mcp:srv\"", got[0].Source)
	}
	if got[1].ServerName != "gleipnir" || got[1].ToolName != "ask_operator" {
		t.Errorf("got[1] = %+v, want gleipnir.ask_operator", got[1])
	}
	if got[1].Source != "" {
		t.Errorf("synthetic tool Source = %q, want empty", got[1].Source)
	}
	if got[2].Source != "plugin:inst@1" {
		t.Errorf("got[2].Source = %q, want \"plugin:inst@1\"", got[2].Source)
	}
}

func TestBuildCapabilitySnapshotTools_FeedbackDisabled(t *testing.T) {
	// When feedbackEnabled is false, ask_operator must not appear.
	mcpTools := []mcp.ResolvedTool{
		{GrantedTool: model.GrantedTool{ServerName: "srv", ToolName: "read"}},
	}
	got := buildCapabilitySnapshotTools(mcpTools, nil, false)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (no ask_operator)", len(got))
	}
	for _, g := range got {
		if g.ToolName == "ask_operator" {
			t.Error("ask_operator found in snapshot with feedbackEnabled=false")
		}
	}
}

func TestBuildCapabilitySnapshotTools_MCPOnly(t *testing.T) {
	mcpTools := []mcp.ResolvedTool{
		{GrantedTool: model.GrantedTool{ServerName: "a", ToolName: "t1"}},
		{GrantedTool: model.GrantedTool{ServerName: "b", ToolName: "t2"}},
	}
	got := buildCapabilitySnapshotTools(mcpTools, nil, false)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
}

func TestBuildCapabilitySnapshotTools_PluginOnly(t *testing.T) {
	pluginGranted := []model.GrantedTool{
		{ToolName: "p1", Source: "plugin:inst@1"},
	}
	got := buildCapabilitySnapshotTools(nil, pluginGranted, false)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Source != "plugin:inst@1" {
		t.Errorf("Source = %q, want \"plugin:inst@1\"", got[0].Source)
	}
}

func TestBuildCapabilitySnapshotTools_ApprovalAndTimeoutPreserved(t *testing.T) {
	// Approval mode and timeout from the GrantedTool must survive the copy.
	mcpTools := []mcp.ResolvedTool{
		{
			GrantedTool: model.GrantedTool{
				ServerName: "srv",
				ToolName:   "gated",
				Approval:   model.ApprovalModeRequired,
				Timeout:    5 * time.Second,
			},
		},
	}
	got := buildCapabilitySnapshotTools(mcpTools, nil, false)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Approval != model.ApprovalModeRequired {
		t.Errorf("Approval = %v, want required", got[0].Approval)
	}
	if got[0].Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want 5s", got[0].Timeout)
	}
}
