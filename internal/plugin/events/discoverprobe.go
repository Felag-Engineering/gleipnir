// discoverprobe.go supplies the production internal/plugin/caphealth.DiscoverProbe
// (ADR-054, mcp-realignment-spec.md §5; #903) -- the piece caphealth's own
// package doc names as machinery with no implementation: DriftDetail,
// applyEventDrift, and the DiscoverProbe interface all existed before this
// file did, waiting for something willing to actually speak `io.gleipnir/events`
// over HTTP.
//
// That something cannot live inside caphealth itself. caphealth is imported
// by things that must never acquire an internal/mcp dependency transitively
// (see caphealth's TestNoMCPImport), and the only way to satisfy
// DiscoverProbe for real is to call internal/mcp's Client -- so the
// implementation lives here, in its own package, and caphealth only ever
// sees it through the narrow interface it already declared.
//
// DiscoverProbe.Discover resolves a plugin instance to its managed MCP
// endpoint (mcp/managed.go's one-row-per-instance registry entry), then
// performs the same server/discover round trip ProbeProtocolVersion already
// makes for protocol pinning -- reused here rather than duplicated, because
// it is the single call that both re-establishes liveness for the caller and
// carries the io.gleipnir/events capability declaration (version, and
// whether the extension was declared at all). Only when that declaration is
// present AND at a major version this host can read does a second call,
// events/discover, go out to list the actual kinds -- there is no way to
// fold that into the first round trip, because events/discover is a
// distinct JSON-RPC method with its own response shape (mcp/events.go).
//
// Three outcomes short-circuit before ever reaching events/discover, and
// each is a caphealth.DiscoverResult shape rather than a Go error, because
// none of them mean "the probe failed" -- they mean "here is what the probe
// learned":
//
//   - the extension was never declared: DiscoverResult{} (its zero value).
//     Not a fault on its own -- most plugins have nothing to say about
//     events -- caphealth.applyEventDrift only turns it into one when the
//     manifest attested the event_source profile.
//   - the extension was declared at a major version this host cannot read:
//     DiscoverResult{ExtensionDeclared:true, VersionRefused:true,
//     DeclaredVersion:<what it said}. Refused, not guessed at -- mirrors
//     internal/plugin/hitl's majorVersionSupported gate for the sibling
//     io.gleipnir/channel extension.
//   - the extension was declared at a readable version: events/discover
//     is called and its kinds populate DiscoverResult.EventKinds.
//
// A genuine transport failure at either round trip IS returned as a Go
// error, and caphealth's prober treats a DiscoverProbe error as a liveness
// fault (server/discover, after all, is also how liveness is established) --
// the same "can't tell drift from unreachable" reasoning that keeps this
// probe from trying to distinguish the two itself.
package events

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
)

// InstanceServerLookup resolves a plugin instance to its managed
// `mcp_servers` row. *db.Queries satisfies it via GetMCPServerByPluginInstance
// -- the same lookup internal/mcp's own managedStore seam uses
// (mcp/managed.go) to find a managed endpoint by instance, reused here rather
// than re-implemented as a second query against the same table.
type InstanceServerLookup interface {
	GetMCPServerByPluginInstance(ctx context.Context, pluginInstanceID *string) (db.McpServer, error)
}

// ClientResolver resolves a managed server row's ID to a ready, cached MCP
// client. *mcp.Registry satisfies it via ClientForServerID -- built for the
// Tasks poll scheduler, where the caller likewise already has a server ID in
// hand rather than a dot-name tool to look up first, which is exactly this
// probe's situation too.
type ClientResolver interface {
	ClientForServerID(ctx context.Context, serverID string) (*mcp.Client, error)
}

// DiscoverProbe implements caphealth.DiscoverProbe against a real managed
// plugin instance's MCP endpoint. See the file doc for the shape of what it
// returns and why.
type DiscoverProbe struct {
	servers InstanceServerLookup
	clients ClientResolver
}

// NewDiscoverProbe returns a probe backed by servers and clients. Neither may
// be nil.
func NewDiscoverProbe(servers InstanceServerLookup, clients ClientResolver) (*DiscoverProbe, error) {
	if servers == nil {
		return nil, errors.New("events: discover probe requires an instance-to-server lookup")
	}
	if clients == nil {
		return nil, errors.New("events: discover probe requires a client resolver")
	}
	return &DiscoverProbe{servers: servers, clients: clients}, nil
}

// Discover satisfies caphealth.DiscoverProbe. See the file doc.
func (p *DiscoverProbe) Discover(ctx context.Context, instanceID string) (caphealth.DiscoverResult, error) {
	srv, err := p.servers.GetMCPServerByPluginInstance(ctx, &instanceID)
	if err != nil {
		return caphealth.DiscoverResult{}, fmt.Errorf("events: resolve managed endpoint for instance %s: %w", instanceID, err)
	}

	client, err := p.clients.ClientForServerID(ctx, srv.ID)
	if err != nil {
		return caphealth.DiscoverResult{}, fmt.Errorf("events: resolve mcp client for instance %s: %w", instanceID, err)
	}

	probe, err := client.ProbeProtocolVersion(ctx)
	if err != nil {
		return caphealth.DiscoverResult{}, fmt.Errorf("events: server/discover for instance %s: %w", instanceID, err)
	}

	if !probe.EventsDeclared {
		return caphealth.DiscoverResult{}, nil
	}

	if !majorVersionSupported(probe.Events.Version) {
		return caphealth.DiscoverResult{
			ExtensionDeclared: true,
			VersionRefused:    true,
			DeclaredVersion:   probe.Events.Version,
		}, nil
	}

	kinds, err := client.DiscoverEventKinds(ctx)
	if err != nil {
		return caphealth.DiscoverResult{}, fmt.Errorf("events: events/discover for instance %s: %w", instanceID, err)
	}

	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = k.Kind
	}
	return caphealth.DiscoverResult{
		ExtensionDeclared: true,
		EventKinds:        names,
	}, nil
}

// majorVersionSupported reports whether a declared io.gleipnir/events
// contract version is one this client can read.
//
// Mirrors internal/plugin/hitl's majorVersionSupported for the sibling
// io.gleipnir/channel extension: minor and patch are additive by policy
// (mcp/events.go's ExtensionEventsVersion doc), so only the major matters,
// and an absent or unparseable version is refused rather than guessed at --
// a health fault built from a contract shape this host cannot establish
// would be a guess dressed up as a fact.
func majorVersionSupported(declared string) bool {
	want, ok := majorOf(mcp.ExtensionEventsVersion)
	if !ok {
		return false
	}
	got, ok := majorOf(declared)
	return ok && got == want
}

func majorOf(version string) (int, bool) {
	major, _, _ := strings.Cut(strings.TrimSpace(version), ".")
	if major == "" {
		return 0, false
	}
	n, err := strconv.Atoi(major)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
