package hostendpoint

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// Tier-2 methods (spec §8.2), ported from internal/plugin/hostsvc's gRPC
// handlers (#889 tracked the tier-1 set; this is the follow-on for the two
// capability-gated methods). Unlike Tier-1, these are OFF by default: a
// plugin instance only reaches either handler when its manifest declares the
// matching entry in tier2_capabilities, and every refusal is audited — the
// audit event is not decoration, it is how an operator learns a plugin
// reached for something it was not granted.

// EventTypeUnauthorizedTier2Call mirrors hostsvc's audit event of the same
// name: a plugin called a Tier-2 tool without declaring the corresponding
// capability in its manifest.
const EventTypeUnauthorizedTier2Call = "unauthorized_tier2_call"

// Tier2Querier is the slice of the sqlc surface the two Tier-2 tools need.
// *db.Queries satisfies it.
type Tier2Querier interface {
	GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error)
	GetPluginByID(ctx context.Context, id string) (db.Plugin, error)
	ListPolicies(ctx context.Context) ([]db.Policy, error)
	ListRunsByPolicies(ctx context.Context, arg db.ListRunsByPoliciesParams) ([]db.ListRunsByPoliciesRow, error)
	ListAllActiveUsersWithRoles(ctx context.Context) ([]db.ListAllActiveUsersWithRolesRow, error)
	ListActiveUsersByRole(ctx context.Context, role string) ([]db.ListActiveUsersByRoleRow, error)
	InsertPluginAuditEvent(ctx context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error)
}

// Tier2Deps carries the collaborators the two handlers share.
type Tier2Deps struct {
	Querier Tier2Querier
}

// Tier2Tools returns the two capability-gated Tier-2 methods as host-endpoint
// tool definitions, ready for Server.Register.
func Tier2Tools(deps Tier2Deps) []ToolDef {
	return []ToolDef{
		{
			Name:        ToolRunHistoryRead,
			Description: "Return past runs for policies that grant the calling instance a tool. Requires the run_history_read Tier-2 capability.",
			Handler:     deps.runHistoryRead,
		},
		{
			Name:        ToolUserDirectoryRead,
			Description: "Return (user_id, username, role) tuples for active users. Requires the user_directory_read Tier-2 capability.",
			Handler:     deps.userDirectoryRead,
		},
	}
}

// resolveInstance fetches the authenticated caller's instance row. Duplicated
// from Tier1Deps rather than shared: the two dep structs are deliberately
// independent so a caller wiring only Tier-2 (or only Tier-1) never needs the
// other's collaborators.
func (d Tier2Deps) resolveInstance(ctx context.Context) (db.PluginInstance, error) {
	id, ok := IdentityFromContext(ctx)
	if !ok {
		return db.PluginInstance{}, &ToolError{Code: "unauthenticated", Message: "no plugin instance identity on request"}
	}
	inst, err := d.Querier.GetPluginInstanceByID(ctx, id.InstanceID)
	if err != nil {
		return db.PluginInstance{}, &ToolError{Code: "internal", Message: fmt.Sprintf("fetch instance: %v", err)}
	}
	return inst, nil
}

// hasTier2Capability checks whether the manifest for inst's parent plugin
// declares the given Tier-2 capability. It reads manifest_snapshot fresh per
// call — no caching — so hot-reload invalidation (spec §5.4) is automatic.
func (d Tier2Deps) hasTier2Capability(ctx context.Context, inst db.PluginInstance, capability string) (bool, *ToolError) {
	plugin, err := d.Querier.GetPluginByID(ctx, inst.PluginID)
	if err != nil {
		return false, &ToolError{Code: "internal", Message: fmt.Sprintf("fetch plugin: %v", err)}
	}
	var m sdkmanifest.Manifest
	if err := yaml.Unmarshal([]byte(plugin.ManifestSnapshot), &m); err != nil {
		return false, &ToolError{Code: "internal", Message: fmt.Sprintf("parse manifest snapshot: %v", err)}
	}
	return m.HasTier2(capability), nil
}

// requireTier2Capability enforces the Tier-2 gate (spec §8.2). When the
// manifest does not declare capability it writes an unauthorized_tier2_call
// audit event and returns the PermissionDenied ToolError — the audit event is
// the whole point of the gate: an operator learns a plugin tried something it
// was never granted, not just that the call failed.
func (d Tier2Deps) requireTier2Capability(ctx context.Context, inst db.PluginInstance, capability, toolName string) *ToolError {
	hasCap, te := d.hasTier2Capability(ctx, inst, capability)
	if te != nil {
		return te
	}
	if hasCap {
		return nil
	}
	d.writeAuditEvent(ctx, inst.ID, EventTypeUnauthorizedTier2Call, "high", map[string]string{
		"tool":       toolName,
		"capability": capability,
	})
	return &ToolError{Code: "permission_denied", Message: EventTypeUnauthorizedTier2Call}
}

// runHistoryReadArgs is the tools/call arguments shape for host/run_history_read.
type runHistoryReadArgs struct {
	PolicyID string `json:"policy_id"`
	Limit    int64  `json:"limit"`
}

// runSummary is one entry of host/run_history_read's response, field names
// carried over from hostsvc's RunSummary proto message.
type runSummary struct {
	RunID      string `json:"run_id"`
	PolicyID   string `json:"policy_id"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
}

// runHistoryRead returns past runs for policies that grant the calling
// instance a tool, i.e. reference it in capabilities.tools via the
// "<instance>.<tool>" namespace prefix (spec §8.2). The scoping is the part
// that must survive the port unchanged: without it this becomes "read all
// run history," which is a different, much larger grant than what the
// capability actually authorizes.
func (d Tier2Deps) runHistoryRead(ctx context.Context, args json.RawMessage) (any, error) {
	inst, err := d.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}
	if te := d.requireTier2Capability(ctx, inst, sdkmanifest.Tier2RunHistoryRead, ToolRunHistoryRead); te != nil {
		return nil, te
	}

	var a runHistoryReadArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, &ToolError{Code: "invalid_argument", Message: fmt.Sprintf("decode arguments: %v", err)}
		}
	}

	scopedIDs, err := d.policyIDsForInstance(ctx, inst)
	if err != nil {
		return nil, err
	}

	// If the caller requested a specific policy, intersect with the scoped
	// set. An out-of-scope policy_id returns an empty list, not an error, so
	// the response does not leak whether the policy exists at all.
	if a.PolicyID != "" {
		var filtered []string
		for _, id := range scopedIDs {
			if id == a.PolicyID {
				filtered = append(filtered, id)
				break
			}
		}
		scopedIDs = filtered
	}

	limit := a.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}

	// Guard against empty scope: ListRunsByPolicies with zero IDs would hit
	// IN (NULL), which is valid SQL but a pointless round trip.
	if len(scopedIDs) == 0 {
		return map[string]any{"runs": []runSummary{}}, nil
	}

	rows, err := d.Querier.ListRunsByPolicies(ctx, db.ListRunsByPoliciesParams{
		PolicyIds: scopedIDs,
		Limit:     limit,
	})
	if err != nil {
		return nil, &ToolError{Code: "internal", Message: fmt.Sprintf("list runs: %v", err)}
	}

	runs := make([]runSummary, 0, len(rows))
	for _, r := range rows {
		finishedAt := ""
		if r.CompletedAt != nil {
			finishedAt = *r.CompletedAt
		}
		runs = append(runs, runSummary{
			RunID:      r.ID,
			PolicyID:   r.PolicyID,
			Status:     r.Status,
			StartedAt:  r.StartedAt,
			FinishedAt: finishedAt,
		})
	}
	return map[string]any{"runs": runs}, nil
}

// scopeProbe pulls only the fields needed to decide whether a policy
// references the calling instance, avoiding a dependency on internal/policy
// (which would create an import cycle). Mirrors hostsvc's scopeProbe.
type scopeProbe struct {
	Capabilities struct {
		Tools []struct {
			Tool string `yaml:"tool"`
		} `yaml:"tools"`
	} `yaml:"capabilities"`
	Trigger struct {
		Type   string `yaml:"type"`
		Source string `yaml:"source"`
	} `yaml:"trigger"`
}

// policyIDsForInstance returns the IDs of policies that reference inst via
// tool grants (capabilities.tools contains an entry with the prefix
// "<instanceName>.") OR via a subscribed trigger (trigger.type == "subscribed"
// and trigger.source == instanceName). A policy reachable through both paths
// appears exactly once.
//
// strings.Cut on "." is deliberate: an empty instance name must not match
// anything, and an instance named "foo" must not match tools intended for
// "foobar" — only the first dot-segment is compared.
func (d Tier2Deps) policyIDsForInstance(ctx context.Context, inst db.PluginInstance) ([]string, error) {
	policies, err := d.Querier.ListPolicies(ctx)
	if err != nil {
		return nil, &ToolError{Code: "internal", Message: fmt.Sprintf("list policies: %v", err)}
	}

	instanceName := inst.InstanceName
	var ids []string
	for _, pol := range policies {
		var probe scopeProbe
		if err := yaml.Unmarshal([]byte(pol.Yaml), &probe); err != nil {
			// Corrupt policy YAML is not a reason to fail the whole call —
			// skip it.
			continue
		}

		matched := false
		for _, t := range probe.Capabilities.Tools {
			ns, _, ok := strings.Cut(t.Tool, ".")
			if ok && ns == instanceName {
				matched = true
				break
			}
		}
		if !matched &&
			probe.Trigger.Type == string(model.TriggerTypeSubscribed) &&
			probe.Trigger.Source == instanceName {
			matched = true
		}
		if matched {
			ids = append(ids, pol.ID)
		}
	}
	return ids, nil
}

// userDirectoryReadArgs is the tools/call arguments shape for
// host/user_directory_read.
type userDirectoryReadArgs struct {
	RoleFilter string `json:"role_filter"`
}

// userEntry is one entry of host/user_directory_read's response. Exactly
// three fields on purpose — user_id, username, role — no credentials, no
// session data. A future field addition to this shape must be a deliberate
// edit here, not an accident of adding a column upstream and forwarding it.
type userEntry struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

var validUserRoles = map[string]bool{
	"admin": true, "operator": true, "approver": true, "auditor": true,
}

// userDirectoryRead returns (user_id, username, role) tuples for active
// users, optionally filtered to one role. Requires the user_directory_read
// Tier-2 capability (spec §8.2).
func (d Tier2Deps) userDirectoryRead(ctx context.Context, args json.RawMessage) (any, error) {
	inst, err := d.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}
	if te := d.requireTier2Capability(ctx, inst, sdkmanifest.Tier2UserDirectoryRead, ToolUserDirectoryRead); te != nil {
		return nil, te
	}

	var a userDirectoryReadArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, &ToolError{Code: "invalid_argument", Message: fmt.Sprintf("decode arguments: %v", err)}
		}
	}
	if a.RoleFilter != "" && !validUserRoles[a.RoleFilter] {
		return nil, &ToolError{Code: "invalid_argument", Message: fmt.Sprintf("unknown role %q", a.RoleFilter)}
	}

	entries := []userEntry{}
	if a.RoleFilter == "" {
		rows, err := d.Querier.ListAllActiveUsersWithRoles(ctx)
		if err != nil {
			return nil, &ToolError{Code: "internal", Message: fmt.Sprintf("list users: %v", err)}
		}
		for _, r := range rows {
			entries = append(entries, userEntry{UserID: r.UserID, Username: r.Username, Role: r.Role})
		}
	} else {
		rows, err := d.Querier.ListActiveUsersByRole(ctx, a.RoleFilter)
		if err != nil {
			return nil, &ToolError{Code: "internal", Message: fmt.Sprintf("list users by role: %v", err)}
		}
		// ListActiveUsersByRoleRow has no Role field; stamp it from the
		// request, since the WHERE clause already filtered to this role.
		for _, r := range rows {
			entries = append(entries, userEntry{UserID: r.UserID, Username: r.Username, Role: a.RoleFilter})
		}
	}

	return map[string]any{"users": entries}, nil
}

// writeAuditEvent inserts a plugin_audit_events row, non-fatally: an audit
// insert failure is logged, never allowed to mask the rejection it records.
// Duplicated from Tier1Deps's helper of the same name — see the
// resolveInstance comment for why the two dep structs stay independent.
func (d Tier2Deps) writeAuditEvent(ctx context.Context, iid, eventType, severity string, payload map[string]string) {
	p, err := json.Marshal(payload)
	if err != nil {
		p = []byte("{}")
	}
	if _, insertErr := d.Querier.InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
		PluginInstanceID: &iid,
		EventType:        eventType,
		Severity:         severity,
		PayloadJson:      string(p),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
	}); insertErr != nil {
		slog.WarnContext(ctx, "audit event insert failed",
			"event_type", eventType, "instance_id", iid, "err", insertErr)
	}
}
