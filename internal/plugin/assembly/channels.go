package assembly

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/plugin/egress"
	"github.com/felag-engineering/gleipnir/internal/plugin/events"
	"github.com/felag-engineering/gleipnir/internal/plugin/hitl"
)

// channelLookupTimeout bounds one instance-to-client resolution. The
// hitl resolver interfaces carry no context, so the lookup runs on the
// assembly's own and needs its own bound.
const channelLookupTimeout = 5 * time.Second

// managedClients resolves a plugin instance to the *mcp.Client for its managed
// endpoint, the same way the events supervisor and the discover probe do:
// GetMCPServerByPluginInstance, then ClientForServerID on the shared registry.
//
// That single path is the point. The assurance gate in hitl.Router asks the
// client for its declared channel capability, and mcp.PollScheduler later
// polls the task through ClientForServerID; if the gate resolved its client any
// other way (a cache keyed by instance ID, say), the two could hold different
// *mcp.Client values for one instance after a rotation repoints the row's url,
// and the gate would vouch for a server the poll is no longer talking to.
type managedClients struct {
	ctx     context.Context
	servers events.InstanceServerLookup
	clients events.ClientResolver
}

func (m managedClients) clientFor(instanceID string) (*mcp.Client, error) {
	ctx, cancel := context.WithTimeout(m.ctx, channelLookupTimeout)
	defer cancel()

	srv, err := m.servers.GetMCPServerByPluginInstance(ctx, &instanceID)
	if err != nil {
		return nil, fmt.Errorf("resolve managed endpoint for instance %s: %w", instanceID, err)
	}
	client, err := m.clients.ClientForServerID(ctx, srv.ID)
	if err != nil {
		return nil, fmt.Errorf("resolve mcp client for instance %s: %w", instanceID, err)
	}
	return client, nil
}

// requestClients is the hitl.ClientResolver (Request leg of the channel
// extension). Its method name collides with notifyClients', whose return type
// differs, so each leg gets its own type over the shared lookup.
type requestClients struct{ managedClients }

func (r requestClients) ChannelClientFor(instanceID string) (hitl.ChannelClient, error) {
	client, err := r.clientFor(instanceID)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// notifyClients is the hitl.NotifyClientResolver (Notify leg).
type notifyClients struct{ managedClients }

func (n notifyClients) ChannelClientFor(instanceID string) (hitl.NotifyClient, error) {
	client, err := n.clientFor(instanceID)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// egressAuditEventType is the plugin_audit_events row an egress refusal writes.
const egressAuditEventType = "plugin_egress_denied"

// maxAuditedHostLen bounds the target host recorded for a refusal. The host is
// whatever the plugin put on the CONNECT line, so it is untrusted input headed
// for an operator-facing feed.
const maxAuditedHostLen = 255

func truncateHost(host string) string {
	if len(host) <= maxAuditedHostLen {
		return host
	}
	return host[:maxAuditedHostLen] + "..."
}

// egressAuditor persists every egress refusal. The proxy logs and counts them
// itself; this is the durable copy an operator reads in the audit feed.
type egressAuditor struct {
	store interface {
		InsertPluginAuditEvent(ctx context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error)
	}
}

func (a egressAuditor) EgressDenied(ctx context.Context, instanceID, host string, reason egress.DenyReason) {
	payload, err := json.Marshal(map[string]string{"host": truncateHost(host), "reason": string(reason)})
	if err != nil {
		payload = []byte("{}")
	}
	params := db.InsertPluginAuditEventParams{
		EventType:   egressAuditEventType,
		Severity:    "warning",
		PayloadJson: string(payload),
		CreatedAt:   timeNow().UTC().Format(time.RFC3339Nano),
	}
	// An unattributable refusal (unknown instance) has no row to point at.
	if instanceID != "" {
		params.PluginInstanceID = &instanceID
	}
	if _, err := a.store.InsertPluginAuditEvent(ctx, params); err != nil {
		slog.WarnContext(ctx, "writing egress denial audit event failed",
			"instance_id", instanceID, "host", host, "reason", string(reason), "err", err)
	}
}
