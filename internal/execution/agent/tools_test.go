package agent

// Tests for the helper functions extracted from the plugin-tool processing loop
// in New() and the snapshot assembly in Run(). The integration guard
// (TestCapabilitySnapshot_IncludesPluginSourceTools) lives in agent_test.go and
// exercises the full BoundAgent loop; these unit tests exercise the helpers in
// isolation so the sync invariant and snapshot ordering have a focused surface.

import (
	"encoding/json"
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

// TestBuildResolvedToolMap_NarrowsCanonicalSchema locks that narrowing runs on
// the tool's stored canonical schema, falling back to the raw InputSchema only
// when no canonical form is stored (mcp.ResolvedTool.SchemaForNarrowing). The
// raw and canonical schemas below declare DIFFERENT property sets so a test
// failure proves which one narrowing actually read.
func TestBuildResolvedToolMap_NarrowsCanonicalSchema(t *testing.T) {
	rawSchema := json.RawMessage(`{"type":"object","properties":{"a":{},"raw_only":{}}}`)
	canonicalSchema := json.RawMessage(`{"type":"object","properties":{"a":{},"canon_only":{}}}`)

	tests := []struct {
		name            string
		canonicalSchema json.RawMessage
		params          map[string]any
		wantKeys        []string
		wantByteEqual   json.RawMessage // when non-nil, narrowedSchema must be byte-equal to this
	}{
		{
			name:            "canonical present, params scoped to canon_only — canonical schema was narrowed",
			canonicalSchema: canonicalSchema,
			params:          map[string]any{"canon_only": true},
			wantKeys:        []string{"canon_only"},
		},
		{
			name:            "canonical nil, params scoped to raw_only — fallback to raw schema",
			canonicalSchema: nil,
			params:          map[string]any{"raw_only": true},
			wantKeys:        []string{"raw_only"},
		},
		{
			name:            "canonical present, empty params — narrowedSchema is byte-equal to canonical (canonical is the schema of record)",
			canonicalSchema: canonicalSchema,
			params:          nil,
			wantByteEqual:   canonicalSchema,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := mcp.ResolvedTool{
				GrantedTool: model.GrantedTool{
					ServerName: "srv",
					ToolName:   "tool",
					Params:     tc.params,
				},
				InputSchema:     rawSchema,
				CanonicalSchema: tc.canonicalSchema,
			}

			toolsByName, err := buildResolvedToolMap([]mcp.ResolvedTool{rt})
			if err != nil {
				t.Fatalf("buildResolvedToolMap: %v", err)
			}
			entry, ok := toolsByName["srv.tool"]
			if !ok {
				t.Fatal("toolsByName missing \"srv.tool\"")
			}

			if tc.wantByteEqual != nil {
				if string(entry.narrowedSchema) != string(tc.wantByteEqual) {
					t.Errorf("narrowedSchema = %s, want byte-equal to %s", entry.narrowedSchema, tc.wantByteEqual)
				}
			} else {
				assertSchemaProperties(t, entry.narrowedSchema, tc.wantKeys)
			}

			// tool.InputSchema must never be mutated by narrowing.
			if string(entry.tool.InputSchema) != string(rawSchema) {
				t.Errorf("entry.tool.InputSchema = %s, want unmodified %s", entry.tool.InputSchema, rawSchema)
			}
		})
	}
}
