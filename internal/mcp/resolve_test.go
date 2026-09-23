package mcp

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

func TestResolveForPolicy_AllToolsFound(t *testing.T) {
	reg, store := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "read_pods", "description": "list pods", "inputSchema": map[string]any{"type": "object"}},
		{"name": "delete_pod", "description": "delete a pod", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if _, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				{Tool: "my-server.read_pods", Approval: model.ApprovalModeNone, Params: map[string]any{"namespace": "worker-01"}},
				{
					Tool:      "my-server.delete_pod",
					Approval:  model.ApprovalModeRequired,
					Timeout:   "30m",
					OnTimeout: model.OnTimeoutReject,
					Params:    map[string]any{"namespace": "worker-01"},
				},
			},
		},
	}

	result, err := reg.ResolveForPolicy(context.Background(), p)
	if err != nil {
		t.Fatalf("ResolveForPolicy: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}

	tool0 := result[0]
	if tool0.ServerName != "my-server" {
		t.Errorf("result[0].ServerName = %q, want %q", tool0.ServerName, "my-server")
	}
	if tool0.ToolName != "read_pods" {
		t.Errorf("result[0].ToolName = %q, want %q", tool0.ToolName, "read_pods")
	}
	if tool0.Approval != model.ApprovalModeNone {
		t.Errorf("result[0].Approval = %q, want %q", tool0.Approval, model.ApprovalModeNone)
	}
	if tool0.Timeout != 0 {
		t.Errorf("result[0].Timeout = %v, want 0", tool0.Timeout)
	}
	if tool0.Client == nil {
		t.Errorf("result[0].Client is nil")
	}

	tool1 := result[1]
	if tool1.Approval != model.ApprovalModeRequired {
		t.Errorf("result[1].Approval = %q, want %q", tool1.Approval, model.ApprovalModeRequired)
	}
	if tool1.Timeout != 30*time.Minute {
		t.Errorf("result[1].Timeout = %v, want %v", tool1.Timeout, 30*time.Minute)
	}
	if tool1.OnTimeout != model.OnTimeoutReject {
		t.Errorf("result[1].OnTimeout = %q, want %q", tool1.OnTimeout, model.OnTimeoutReject)
	}
	if tool1.Client == nil {
		t.Errorf("result[1].Client is nil")
	}
}

func TestResolveForPolicy_MissingTool(t *testing.T) {
	reg, store := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "read_pods", "description": "list pods", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if _, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				{Tool: "my-server.read_pods", Approval: model.ApprovalModeNone},
				{Tool: "my-server.nonexistent_tool", Approval: model.ApprovalModeNone},
			},
		},
	}

	_, err := reg.ResolveForPolicy(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for missing tool, got nil")
	}
	if !strings.Contains(err.Error(), "nonexistent_tool") {
		t.Errorf("error %q does not mention the missing tool name", err.Error())
	}
}

func TestResolveForPolicy_EmptyCapabilities(t *testing.T) {
	reg, _ := newTestRegistry(t)

	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{},
	}

	result, err := reg.ResolveForPolicy(context.Background(), p)
	if err != nil {
		t.Fatalf("ResolveForPolicy: unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("len(result) = %d, want 0", len(result))
	}
}

func TestResolveForPolicy_InvalidDotNotation(t *testing.T) {
	reg, _ := newTestRegistry(t)

	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				{Tool: "nodot", Approval: model.ApprovalModeNone},
			},
		},
	}

	_, err := reg.ResolveForPolicy(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for invalid dot-notation, got nil")
	}
	// Error must mention dot-notation or server.tool format.
	if !strings.Contains(err.Error(), "dot-notation") && !strings.Contains(err.Error(), "server.tool") {
		t.Errorf("error %q does not mention dot-notation or server.tool format", err.Error())
	}
}

func TestResolveForPolicy_ServerNotFound(t *testing.T) {
	reg, _ := newTestRegistry(t)

	// No servers registered — any tool reference should fail.
	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				{Tool: "ghost-server.some_tool", Approval: model.ApprovalModeNone},
			},
		},
	}

	_, err := reg.ResolveForPolicy(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for unregistered server, got nil")
	}
}

func TestResolveForPolicy_ToolNotFound(t *testing.T) {
	reg, store := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "read_pods", "description": "list pods", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if _, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				{Tool: "my-server.read_pods", Approval: model.ApprovalModeNone},
				{Tool: "my-server.ghost_tool", Approval: model.ApprovalModeNone},
			},
		},
	}

	_, err := reg.ResolveForPolicy(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for missing tool, got nil")
	}
	if !strings.Contains(err.Error(), "ghost_tool") {
		t.Errorf("error %q does not mention the missing tool name", err.Error())
	}
}

func TestResolveForPolicy_SharedClient(t *testing.T) {
	reg, store := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "tool_a", "description": "tool a", "inputSchema": map[string]any{"type": "object"}},
		{"name": "tool_b", "description": "tool b", "inputSchema": map[string]any{"type": "object"}},
		{"name": "tool_c", "description": "tool c", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if _, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				{Tool: "my-server.tool_a", Approval: model.ApprovalModeNone},
				{Tool: "my-server.tool_b", Approval: model.ApprovalModeNone},
				{Tool: "my-server.tool_c", Approval: model.ApprovalModeNone},
			},
		},
	}

	result, err := reg.ResolveForPolicy(context.Background(), p)
	if err != nil {
		t.Fatalf("ResolveForPolicy: %v", err)
	}

	if len(result) != 3 {
		t.Fatalf("len(result) = %d, want 3", len(result))
	}

	if result[0].Client != result[1].Client {
		t.Errorf("result[0].Client and result[1].Client are different pointers; want same client for tools on the same server")
	}
	if result[0].Client != result[2].Client {
		t.Errorf("result[0].Client and result[2].Client are different pointers; want same client for tools on the same server")
	}
}

func TestResolveForPolicy_ToolsOrdered(t *testing.T) {
	reg, store := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "tool_a", "description": "tool a", "inputSchema": map[string]any{"type": "object"}},
		{"name": "tool_b", "description": "tool b", "inputSchema": map[string]any{"type": "object"}},
		{"name": "tool_c", "description": "tool c", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if _, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				{Tool: "my-server.tool_a", Approval: model.ApprovalModeNone},
				{Tool: "my-server.tool_b", Approval: model.ApprovalModeNone},
				{Tool: "my-server.tool_c", Approval: model.ApprovalModeRequired},
			},
		},
	}

	result, err := reg.ResolveForPolicy(context.Background(), p)
	if err != nil {
		t.Fatalf("ResolveForPolicy: %v", err)
	}

	if len(result) != 3 {
		t.Fatalf("len(result) = %d, want 3", len(result))
	}
	// Verify results are returned in policy order.
	if result[0].ToolName != "tool_a" {
		t.Errorf("result[0].ToolName = %q, want tool_a", result[0].ToolName)
	}
	if result[1].ToolName != "tool_b" {
		t.Errorf("result[1].ToolName = %q, want tool_b", result[1].ToolName)
	}
	if result[2].ToolName != "tool_c" {
		t.Errorf("result[2].ToolName = %q, want tool_c", result[2].ToolName)
	}
}

func TestResolveForPolicy_DisabledTool(t *testing.T) {
	reg, store := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "read_pods", "description": "list pods", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if _, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	// Fetch the tool ID so we can disable it.
	registered, err := store.Queries().GetMCPToolByServerAndName(context.Background(), db.GetMCPToolByServerAndNameParams{
		ServerName: "my-server",
		ToolName:   "read_pods",
	})
	if err != nil {
		t.Fatalf("GetMCPToolByServerAndName: %v", err)
	}

	if err := store.Queries().SetMCPToolEnabled(context.Background(), db.SetMCPToolEnabledParams{
		ID:      registered.ID,
		Enabled: 0,
	}); err != nil {
		t.Fatalf("SetMCPToolEnabled: %v", err)
	}

	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				{Tool: "my-server.read_pods", Approval: model.ApprovalModeNone},
			},
		},
	}

	_, err = reg.ResolveForPolicy(context.Background(), p)
	if err == nil {
		t.Fatal("expected error for disabled tool, got nil")
	}
	if !strings.Contains(err.Error(), "read_pods") {
		t.Errorf("error %q should mention tool name read_pods", err.Error())
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("error %q should contain the word 'disabled'", err.Error())
	}
}

func TestResolveToolByName_DisabledTool(t *testing.T) {
	reg, store := newTestRegistry(t)

	tools := []map[string]any{
		{"name": "read_pods", "description": "list pods", "inputSchema": map[string]any{"type": "object"}},
	}
	srv := makeMCPServer(t, tools)

	if _, err := RegisterServerForTest(context.Background(), store.Queries(), reg, "my-server", srv.URL); err != nil {
		t.Fatalf("RegisterServerForTest: %v", err)
	}

	registered, err := store.Queries().GetMCPToolByServerAndName(context.Background(), db.GetMCPToolByServerAndNameParams{
		ServerName: "my-server",
		ToolName:   "read_pods",
	})
	if err != nil {
		t.Fatalf("GetMCPToolByServerAndName: %v", err)
	}

	if err := store.Queries().SetMCPToolEnabled(context.Background(), db.SetMCPToolEnabledParams{
		ID:      registered.ID,
		Enabled: 0,
	}); err != nil {
		t.Fatalf("SetMCPToolEnabled: %v", err)
	}

	_, _, err = reg.ResolveToolByName(context.Background(), "my-server.read_pods")
	if err == nil {
		t.Fatal("expected error for disabled tool, got nil")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("error %q should contain the word 'disabled'", err.Error())
	}
}

// TestResolveForPolicy_SameURLServersGetSeparateClientsAndHeaders is the
// regression test for the #928 security review finding: mcp_servers.url is
// not unique, so two distinct server rows can share one url. Before this
// fix, ResolveForPolicy's per-call client cache was keyed by srv.Url, so a
// pinned server and an unpinned server on the same url would share one
// *Client — silently bypassing the pinned server's CA and leaking one
// server's ADR-039 auth headers onto the other's requests. The map is now
// keyed by srv.ID.
//
// The policy grants the UNPINNED server's tool first, matching the order
// that actually triggered the bug: the first resolve populates the (bogus,
// url-keyed) cache entry, and the second resolve for the pinned server would
// have incorrectly reused it.
func TestResolveForPolicy_SameURLServersGetSeparateClientsAndHeaders(t *testing.T) {
	testKey := mustTestKey(t)
	reg, store := newTestRegistryWithKey(t, testKey)

	caA := testutil.NewTestCA(t)
	leafA := caA.IssueServerCert(t, nil, []net.IP{net.ParseIP("127.0.0.1")})
	srv := testutil.StartTLSServer(t, legacyToolHandler("shared-tool"), leafA)

	now := time.Now().UTC().Format(time.RFC3339Nano)

	unpinnedHeaders := mustEncryptHeaders(t, testKey, []AuthHeader{{Name: "X-Api-Key", Value: "unpinned-secret"}})
	unpinnedID := model.NewULID()
	if _, err := store.Queries().CreateMCPServer(context.Background(), db.CreateMCPServerParams{
		ID:                   unpinnedID,
		Name:                 "server-unpinned",
		Url:                  srv.URL,
		CreatedAt:            now,
		AuthHeadersEncrypted: unpinnedHeaders,
	}); err != nil {
		t.Fatalf("CreateMCPServer (unpinned): %v", err)
	}
	if _, err := store.Queries().UpsertMCPTool(context.Background(), db.UpsertMCPToolParams{
		ID: model.NewULID(), ServerID: unpinnedID, Name: "shared-tool", Description: "d",
		InputSchema: `{"type":"object"}`, CreatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertMCPTool (unpinned): %v", err)
	}

	pinnedHeaders := mustEncryptHeaders(t, testKey, []AuthHeader{{Name: "X-Api-Key", Value: "pinned-secret"}})
	pinnedID := model.NewULID()
	if _, err := store.Queries().CreateMCPServer(context.Background(), db.CreateMCPServerParams{
		ID:                   pinnedID,
		Name:                 "server-pinned",
		Url:                  srv.URL, // same url as the unpinned row, deliberately
		CreatedAt:            now,
		AuthHeadersEncrypted: pinnedHeaders,
		CaCertPem:            &caA.PEM,
	}); err != nil {
		t.Fatalf("CreateMCPServer (pinned): %v", err)
	}
	if _, err := store.Queries().UpsertMCPTool(context.Background(), db.UpsertMCPToolParams{
		ID: model.NewULID(), ServerID: pinnedID, Name: "shared-tool", Description: "d",
		InputSchema: `{"type":"object"}`, CreatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertMCPTool (pinned): %v", err)
	}

	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				// Unpinned first: this is the order that triggered the bug.
				{Tool: "server-unpinned.shared-tool", Approval: model.ApprovalModeNone},
				{Tool: "server-pinned.shared-tool", Approval: model.ApprovalModeNone},
			},
		},
	}

	result, err := reg.ResolveForPolicy(context.Background(), p)
	if err != nil {
		t.Fatalf("ResolveForPolicy: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}

	unpinnedClient := result[0].Client
	pinnedClient := result[1].Client

	if unpinnedClient == pinnedClient {
		t.Fatal("server-unpinned and server-pinned share one *Client despite being distinct rows — " +
			"mcp_servers.url is not unique, and sharing a client bypasses the pinned server's CA and " +
			"leaks its auth headers onto the unpinned server's requests (issue #928 security review)")
	}
	if !pinnedClient.pinnedCA {
		t.Error("server-pinned's client is not marked pinnedCA")
	}
	if unpinnedClient.pinnedCA {
		t.Error("server-unpinned's client must not be pinned")
	}

	// Headers must not cross between the two servers. Checked directly on
	// the unexported field (in-package test) rather than by capturing a live
	// request, because the whole point being guarded against is exactly this
	// kind of cross-contamination at the Client level.
	if len(unpinnedClient.authHeaders) != 1 || unpinnedClient.authHeaders[0].Value != "unpinned-secret" {
		t.Errorf("server-unpinned's client authHeaders = %+v, want [{X-Api-Key unpinned-secret}]", unpinnedClient.authHeaders)
	}
	if len(pinnedClient.authHeaders) != 1 || pinnedClient.authHeaders[0].Value != "pinned-secret" {
		t.Errorf("server-pinned's client authHeaders = %+v, want [{X-Api-Key pinned-secret}]", pinnedClient.authHeaders)
	}

	// The pinned client's pin must actually be enforced, not merely recorded:
	// the real backend's certificate IS signed by CA-A, so the call succeeds.
	if _, err := pinnedClient.CallTool(context.Background(), "shared-tool", nil, CallOptions{}); err != nil {
		t.Fatalf("pinned CallTool against its own CA: %v", err)
	}
}

// TestResolveForPolicy_PinRefusesADifferentCA is the companion direction:
// a server pinned to CA-A must refuse a real backend whose certificate is
// signed by a different CA, even though the pin itself is well-formed.
func TestResolveForPolicy_PinRefusesADifferentCA(t *testing.T) {
	reg, store := newTestRegistry(t)

	caA := testutil.NewTestCA(t)
	caB := testutil.NewTestCA(t)
	leafB := caB.IssueServerCert(t, nil, []net.IP{net.ParseIP("127.0.0.1")})
	srv := testutil.StartTLSServer(t, legacyToolHandler("shared-tool"), leafB)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	serverID := model.NewULID()
	if _, err := store.Queries().CreateMCPServer(context.Background(), db.CreateMCPServerParams{
		ID:        serverID,
		Name:      "server-wrong-ca",
		Url:       srv.URL,
		CreatedAt: now,
		CaCertPem: &caA.PEM, // pinned to CA-A, but the backend's leaf is signed by CA-B
	}); err != nil {
		t.Fatalf("CreateMCPServer: %v", err)
	}
	if _, err := store.Queries().UpsertMCPTool(context.Background(), db.UpsertMCPToolParams{
		ID: model.NewULID(), ServerID: serverID, Name: "shared-tool", Description: "d",
		InputSchema: `{"type":"object"}`, CreatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertMCPTool: %v", err)
	}

	p := &model.ParsedPolicy{
		Capabilities: model.CapabilitiesConfig{
			Tools: []model.ToolCapability{
				{Tool: "server-wrong-ca.shared-tool", Approval: model.ApprovalModeNone},
			},
		},
	}

	result, err := reg.ResolveForPolicy(context.Background(), p)
	if err != nil {
		t.Fatalf("ResolveForPolicy: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}

	_, err = result[0].Client.CallTool(context.Background(), "shared-tool", nil, CallOptions{})
	if err == nil {
		t.Fatal("expected CallTool to fail: the pinned CA does not sign this server's certificate")
	}
	var tlsErr *TLSVerificationError
	if !errors.As(err, &tlsErr) {
		t.Errorf("CallTool error = %v (%T), want *TLSVerificationError", err, err)
	}
}
