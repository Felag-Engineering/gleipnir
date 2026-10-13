//go:build substratev2

package assembly

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	runpkg "github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

const (
	v2ToolManifest = `schema_version: "2"
name: slack
version: "1.0.0"
package:
  registry_type: oci
  identifier: ghcr.io/acme/slack@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    tool_provider: {}
`

	v2ChannelOnlyManifest = `schema_version: "2"
name: slack
version: "1.0.0"
package:
  registry_type: oci
  identifier: ghcr.io/acme/slack@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    human_channel:
      assurance: authenticated
`

	v1Manifest = `schema_version: "1"
name: slack
version: "1.0.0"
`
)

// fakeClassifierStore serves one installed instance and lets each case break
// exactly one thing about it.
type fakeClassifierStore struct {
	instance    db.PluginInstance
	instanceErr error
	plugin      db.Plugin
	pluginErr   error
	server      db.McpServer
	serverErr   error
	tool        db.McpTool
	toolErr     error
}

func (f *fakeClassifierStore) GetPluginInstanceByGlobalName(context.Context, string) (db.PluginInstance, error) {
	return f.instance, f.instanceErr
}

func (f *fakeClassifierStore) GetPluginByID(context.Context, string) (db.Plugin, error) {
	return f.plugin, f.pluginErr
}

func (f *fakeClassifierStore) GetMCPServerByPluginInstance(context.Context, *string) (db.McpServer, error) {
	return f.server, f.serverErr
}

func (f *fakeClassifierStore) GetMCPToolByServerAndName(context.Context, db.GetMCPToolByServerAndNameParams) (db.McpTool, error) {
	return f.tool, f.toolErr
}

func healthyClassifierStore() *fakeClassifierStore {
	instanceID := "inst-1"
	return &fakeClassifierStore{
		instance: db.PluginInstance{ID: instanceID, PluginID: "plug-1", InstanceName: "slack-prod"},
		plugin:   db.Plugin{ID: "plug-1", Name: "slack", Status: string(model.PluginStatusActive), ManifestSnapshot: v2ToolManifest},
		server:   db.McpServer{ID: "srv-1", Name: "slack-prod", PluginInstanceID: &instanceID},
		tool:     db.McpTool{ServerID: "srv-1", Name: "post_message"},
	}
}

// The classifier's job on a plugin instance's name is to refuse, so the table
// is mostly the ways a grant must NOT be allowed through.
func TestManagedToolClassifier_IsPluginTool(t *testing.T) {
	otherInstance := "inst-other"

	tests := []struct {
		name    string
		dotName string
		mutate  func(*fakeClassifierStore)
		wantErr string // substring; empty means no error
	}{
		{
			name:    "declared tool on a healthy managed endpoint routes to the MCP path",
			dotName: "slack-prod.post_message",
		},
		{
			name:    "a name without a dot is not a plugin tool",
			dotName: "post_message",
		},
		{
			name:    "an empty tool half is not a plugin tool",
			dotName: "slack-prod.",
		},
		{
			name:    "a source with no installed instance is an external MCP server",
			dotName: "github.create_issue",
			mutate:  func(f *fakeClassifierStore) { f.instanceErr = sql.ErrNoRows },
		},
		{
			name:    "instance lookup failure is an error, not a fall-through",
			dotName: "slack-prod.post_message",
			mutate:  func(f *fakeClassifierStore) { f.instanceErr = errors.New("database is locked") },
			wantErr: "lookup plugin instance",
		},
		{
			name:    "plugin row unreadable",
			dotName: "slack-prod.post_message",
			mutate:  func(f *fakeClassifierStore) { f.pluginErr = errors.New("database is locked") },
			wantErr: "load plugin",
		},
		{
			name:    "plugin pending review",
			dotName: "slack-prod.post_message",
			mutate:  func(f *fakeClassifierStore) { f.plugin.Status = string(model.PluginStatusPendingReview) },
			wantErr: "not active",
		},
		{
			name:    "plugin removed",
			dotName: "slack-prod.post_message",
			mutate:  func(f *fakeClassifierStore) { f.plugin.Status = string(model.PluginStatusRemoved) },
			wantErr: "not active",
		},
		{
			name:    "manifest does not parse",
			dotName: "slack-prod.post_message",
			mutate:  func(f *fakeClassifierStore) { f.plugin.ManifestSnapshot = "schema_version: [unterminated" },
			wantErr: "manifest",
		},
		{
			name:    "v1 manifest is not served on the v2 substrate",
			dotName: "slack-prod.post_message",
			mutate:  func(f *fakeClassifierStore) { f.plugin.ManifestSnapshot = v1Manifest },
			wantErr: "only v2 plugins",
		},
		{
			name:    "manifest never declared the tool_provider profile",
			dotName: "slack-prod.post_message",
			mutate:  func(f *fakeClassifierStore) { f.plugin.ManifestSnapshot = v2ChannelOnlyManifest },
			wantErr: "tool_provider",
		},
		{
			name:    "instance has no managed endpoint",
			dotName: "slack-prod.post_message",
			mutate:  func(f *fakeClassifierStore) { f.serverErr = sql.ErrNoRows },
			wantErr: "no managed endpoint",
		},
		{
			name:    "endpoint lookup failure",
			dotName: "slack-prod.post_message",
			mutate:  func(f *fakeClassifierStore) { f.serverErr = errors.New("database is locked") },
			wantErr: "managed endpoint",
		},
		{
			name:    "endpoint row is not managed",
			dotName: "slack-prod.post_message",
			mutate:  func(f *fakeClassifierStore) { f.server.PluginInstanceID = nil },
			wantErr: "not instance",
		},
		{
			name:    "endpoint is named for a different source",
			dotName: "slack-prod.post_message",
			mutate:  func(f *fakeClassifierStore) { f.server.Name = "something-else" },
			wantErr: "not instance",
		},
		{
			name:    "tool was never discovered on the endpoint",
			dotName: "slack-prod.delete_workspace",
			mutate:  func(f *fakeClassifierStore) { f.toolErr = sql.ErrNoRows },
			wantErr: "has not discovered",
		},
		{
			name:    "tool lookup failure",
			dotName: "slack-prod.post_message",
			mutate:  func(f *fakeClassifierStore) { f.toolErr = errors.New("database is locked") },
			wantErr: "lookup tool",
		},
		{
			name:    "tool row belongs to another server",
			dotName: "slack-prod.post_message",
			mutate:  func(f *fakeClassifierStore) { f.tool.ServerID = "srv-" + otherInstance },
			wantErr: "different endpoint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := healthyClassifierStore()
			if tt.mutate != nil {
				tt.mutate(store)
			}
			c := &managedToolClassifier{q: store}

			isPlugin, err := c.IsPluginTool(context.Background(), tt.dotName)

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("IsPluginTool: unexpected error %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("IsPluginTool error = %v, want one containing %q", err, tt.wantErr)
			}
			// A managed tool is an MCP tool: the classifier never answers true,
			// so a refusal can never be mistaken for routing to a plugin pool.
			if isPlugin {
				t.Error("IsPluginTool = true; managed tools route through the MCP registry")
			}
		})
	}
}

// DefaultToolResolver with the assembly's classifier and no plugin resolver:
// a managed plugin's discovered tool resolves through the MCP registry to a
// client that points at the plugin's endpoint, and a grant the classifier
// refuses fails the whole resolution instead of reaching the registry.
func TestDefaultToolResolver_ManagedTools(t *testing.T) {
	store := testutil.NewTestStore(t)
	q := store.Queries()
	ctx := context.Background()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := q.CreatePlugin(ctx, db.CreatePluginParams{
		ID: "plug-1", Name: "slack", PluginVersion: "1.0.0", ManifestSnapshot: v2ToolManifest,
		TrustedPubkey: "k", Status: "active", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePlugin: %v", err)
	}
	if _, err := q.CreatePluginInstance(ctx, db.CreatePluginInstanceParams{
		ID: "inst-1", PluginID: "plug-1", InstanceName: "slack-prod", ConfigJson: "{}",
		SubscriptionScopeJson: "{}", HandshakeVersions: "{}", HealthState: "healthy",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}
	registrar, err := mcp.NewManagedRegistrar(q, model.NewULID, nil)
	if err != nil {
		t.Fatalf("NewManagedRegistrar: %v", err)
	}
	srv, err := registrar.Register(ctx, mcp.ManagedEndpoint{InstanceID: "inst-1", InstanceName: "slack-prod", URL: "http://10.83.0.2:9000"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	schema := `{"type":"object"}`
	if _, err := q.UpsertMCPTool(ctx, db.UpsertMCPToolParams{
		ID: model.NewULID(), ServerID: srv.ID, Name: "post_message", Description: "post", InputSchema: schema,
		CanonicalSchema: &schema, CreatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertMCPTool: %v", err)
	}

	registry := mcp.NewRegistry(q)
	resolver := runpkg.NewDefaultToolResolver(registry, &managedToolClassifier{q: q}, nil)

	tests := []struct {
		name    string
		grant   string
		wantErr string
		wantMCP int
	}{
		{name: "discovered managed tool resolves through the registry", grant: "slack-prod.post_message", wantMCP: 1},
		{name: "undiscovered tool on a plugin instance is refused", grant: "slack-prod.delete_workspace", wantErr: "has not discovered"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, err := resolver.ResolveCapabilities(ctx, []model.ToolCapability{{Tool: tt.grant}})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ResolveCapabilities error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveCapabilities: %v", err)
			}
			if len(set.MCPTools) != tt.wantMCP || len(set.PluginTools) != 0 {
				t.Fatalf("resolved %d MCP and %d plugin tools, want %d MCP and 0 plugin", len(set.MCPTools), len(set.PluginTools), tt.wantMCP)
			}
			if got := set.MCPTools[0].ServerID; got != srv.ID {
				t.Errorf("tool resolved against server %q, want the instance's managed endpoint %q", got, srv.ID)
			}
		})
	}
}
