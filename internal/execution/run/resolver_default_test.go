package run

// Tests for DefaultToolResolver.ResolveCapabilities. This file uses package run
// (internal) so it can access NewDefaultToolResolver and the stub interfaces
// directly, without going through the public API surface.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// stubRegistry is a test double for registryResolver. It records the grants
// it was called with and returns pre-canned results or errors.
type stubRegistry struct {
	called   bool
	gotTools []model.ToolCapability
	tools    []mcp.ResolvedTool
	err      error
}

func (s *stubRegistry) ResolveForPolicy(_ context.Context, p *model.ParsedPolicy) ([]mcp.ResolvedTool, error) {
	s.called = true
	s.gotTools = p.Capabilities.Tools
	return s.tools, s.err
}

// stubClassifier classifies tools whose dot-name is in pluginTools as plugin-sourced.
// When err is non-nil it is returned for every call.
type stubClassifier struct {
	pluginTools map[string]bool
	err         error
}

func (s *stubClassifier) IsPluginTool(_ context.Context, dotName string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.pluginTools[dotName], nil
}

// stubPluginResolver returns pre-canned PluginToolEntry values or an error.
type stubPluginResolver struct {
	entries []agent.PluginToolEntry
	err     error
}

func (s *stubPluginResolver) ResolvePluginTools(_ context.Context, _ []model.ToolCapability) ([]agent.PluginToolEntry, error) {
	return s.entries, s.err
}

func TestDefaultToolResolver_AllMCP_NilClassifier(t *testing.T) {
	// When classifier is nil, all grants go to the registry; PluginTools is empty.
	grant := model.ToolCapability{Tool: "srv.read"}
	reg := &stubRegistry{tools: []mcp.ResolvedTool{{GrantedTool: model.GrantedTool{ToolName: "read"}}}}
	r := NewDefaultToolResolver(reg, nil, nil)

	got, err := r.ResolveCapabilities(context.Background(), []model.ToolCapability{grant})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reg.called {
		t.Error("registry was not called")
	}
	if len(reg.gotTools) != 1 || reg.gotTools[0].Tool != "srv.read" {
		t.Errorf("registry received wrong grants: %v", reg.gotTools)
	}
	if len(got.MCPTools) != 1 {
		t.Errorf("MCPTools len = %d, want 1", len(got.MCPTools))
	}
	if len(got.PluginTools) != 0 {
		t.Errorf("PluginTools len = %d, want 0", len(got.PluginTools))
	}
}

func TestDefaultToolResolver_EmptyGrants_SkipsRegistry(t *testing.T) {
	// With no grants, the registry should never be called (DB round-trip skip).
	reg := &stubRegistry{}
	r := NewDefaultToolResolver(reg, nil, nil)

	got, err := r.ResolveCapabilities(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reg.called {
		t.Error("registry was called with empty grants — expected skip")
	}
	if len(got.MCPTools) != 0 || len(got.PluginTools) != 0 {
		t.Errorf("expected empty slices, got MCPTools=%d PluginTools=%d", len(got.MCPTools), len(got.PluginTools))
	}
}

func TestDefaultToolResolver_MixedGrants(t *testing.T) {
	// Mixed grants: one MCP tool, one plugin tool. Each goes to the right resolver.
	mcpGrant := model.ToolCapability{Tool: "mcp-srv.read"}
	plugGrant := model.ToolCapability{Tool: "plug.do-thing"}

	mcpTool := mcp.ResolvedTool{GrantedTool: model.GrantedTool{ToolName: "read"}}
	plugEntry := agent.PluginToolEntry{InstanceName: "plug", ToolName: "do-thing"}

	reg := &stubRegistry{tools: []mcp.ResolvedTool{mcpTool}}
	cls := &stubClassifier{pluginTools: map[string]bool{"plug.do-thing": true}}
	plugRes := &stubPluginResolver{entries: []agent.PluginToolEntry{plugEntry}}

	r := NewDefaultToolResolver(reg, cls, plugRes)
	got, err := r.ResolveCapabilities(context.Background(), []model.ToolCapability{mcpGrant, plugGrant})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Registry should have received only the MCP grant.
	if len(reg.gotTools) != 1 || reg.gotTools[0].Tool != "mcp-srv.read" {
		t.Errorf("registry grants = %v, want only mcp-srv.read", reg.gotTools)
	}
	if len(got.MCPTools) != 1 {
		t.Errorf("MCPTools len = %d, want 1", len(got.MCPTools))
	}
	if len(got.PluginTools) != 1 {
		t.Errorf("PluginTools len = %d, want 1", len(got.PluginTools))
	}
}

func TestDefaultToolResolver_ClassifyError(t *testing.T) {
	// A classifier error should return an empty ResolvedToolSet and an error
	// wrapping "classify tool %q".
	classErr := errors.New("manifest load failed")
	cls := &stubClassifier{err: classErr}
	reg := &stubRegistry{}
	r := NewDefaultToolResolver(reg, cls, nil)

	grant := model.ToolCapability{Tool: "some.tool"}
	got, err := r.ResolveCapabilities(context.Background(), []model.ToolCapability{grant})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, classErr) {
		t.Errorf("error chain does not wrap classErr: %v", err)
	}
	// Should contain the tool name in the message.
	if err.Error() == "" {
		t.Error("error message is empty")
	}
	// Registry must not be called — no partial resolution on classify error.
	if reg.called {
		t.Error("registry was called despite classify error")
	}
	// ResolvedToolSet must be empty (zero value).
	if len(got.MCPTools) != 0 || len(got.PluginTools) != 0 {
		t.Errorf("expected empty ResolvedToolSet, got MCPTools=%d PluginTools=%d", len(got.MCPTools), len(got.PluginTools))
	}
}

func TestDefaultToolResolver_PluginGrantNilResolver(t *testing.T) {
	// A plugin-classified grant with a nil pluginResolver must fail with a
	// clear "subsystem not enabled" error.
	cls := &stubClassifier{pluginTools: map[string]bool{"plug.thing": true}}
	reg := &stubRegistry{}
	r := NewDefaultToolResolver(reg, cls, nil) // nil pluginResolver

	grant := model.ToolCapability{Tool: "plug.thing"}
	_, err := r.ResolveCapabilities(context.Background(), []model.ToolCapability{grant})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	wantSub := "plugin subsystem is not enabled"
	if !strings.Contains(err.Error(), wantSub) {
		t.Errorf("error %q does not contain %q", err.Error(), wantSub)
	}
}

func TestDefaultToolResolver_MCPResolveError(t *testing.T) {
	// When the registry returns an error, it must propagate unwrapped.
	regErr := errors.New("mcp server offline")
	reg := &stubRegistry{err: regErr}
	r := NewDefaultToolResolver(reg, nil, nil)

	grant := model.ToolCapability{Tool: "srv.read"}
	_, err := r.ResolveCapabilities(context.Background(), []model.ToolCapability{grant})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, regErr) {
		t.Errorf("error chain does not wrap regErr: %v", err)
	}
}

func TestDefaultToolResolver_PluginResolveError(t *testing.T) {
	// When the plugin resolver returns an error, it must propagate unwrapped.
	plugErr := errors.New("instance not running")
	cls := &stubClassifier{pluginTools: map[string]bool{"plug.thing": true}}
	plugRes := &stubPluginResolver{err: plugErr}
	r := NewDefaultToolResolver(&stubRegistry{}, cls, plugRes)

	grant := model.ToolCapability{Tool: "plug.thing"}
	_, err := r.ResolveCapabilities(context.Background(), []model.ToolCapability{grant})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, plugErr) {
		t.Errorf("error chain does not wrap plugErr: %v", err)
	}
}
