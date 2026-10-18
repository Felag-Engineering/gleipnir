package hostendpoint

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/actordir"
)

// host/authorize_actor (spec §6.4, §8) is the method ADR-055's audience
// router has been waiting on (internal/plugin/hitl/route.go): the click-time
// pre-check a plugin channel runs before it lets an externally-asserted
// identity (a Slack user.id, or similar) settle a pending HITL ask, plus the
// poll-now hint that closes the resolution-latency gap to the old in-memory
// gRPC waiter (Amendment 1).
//
// What this replaces: v1.1's WriteAuditStep bolted the same check onto a
// write-then-refuse RPC — an actor_external_id rode along on the
// feedback_response call, and the host resolved the external id, checked the
// mapped user's role, and only THEN either completed the response or
// rejected it after the fact. The realigned flow inverts that to
// pre-check: the plugin calls AuthorizeActor first, and only a plugin that
// gets authorized:true back goes on to complete its own task with
// {option_id, actor_external_id}. The role gate and the audit event are the
// same logic, ported rather than redesigned — hostsvc's
// authorizeExternalActor (internal/plugin/hostsvc/handlers_feedback.go) is
// the precedent this file mirrors.
//
// What this does NOT do: resolve or settle anything. The spec's shape for
// this method is "authorize, then hint" — settlement of the underlying
// mcp_tasks row stays entirely with the task/CAS machinery
// (internal/mcp.PollScheduler, inapptask.Manager.Complete). Folding
// resolution into this call would let an authorization RPC double as a write
// path with none of the CAS discipline the real completion goes through, and
// would leave two different call shapes both claiming to be "the" way an
// answer lands.

// EventTypeUnauthorizedApproval mirrors hostsvc's audit event of the same
// name (internal/plugin/hostsvc/audit_guard.go). Declared again here, rather
// than imported, because hostsvc is the gRPC host-RPC plane being retired at
// the #883 cutover — this port must not take a dependency on code already
// scheduled for deletion, even for a shared string constant.
const EventTypeUnauthorizedApproval = "unauthorized_approval_attempt"

// authorizedActorRoles are the roles that may resolve a tool-initiated HITL
// request via an externally-asserted actor identity. Auditor is deliberately
// excluded — ported verbatim from hostsvc.authorizedActorRoles (#624): an
// auditor's job is to observe the record, not to become part of it.
var authorizedActorRoles = map[model.Role]bool{
	model.RoleApprover: true,
	model.RoleOperator: true,
	model.RoleAdmin:    true,
}

// hasAuthorizedRole reports whether roles contains one of
// approver/operator/admin.
func hasAuthorizedRole(roles []model.Role) bool {
	for _, r := range roles {
		if authorizedActorRoles[r] {
			return true
		}
	}
	return false
}

// PollHint is the spec §6.4 Amendment 1 poll-now signal: on a successful
// authorization, the host polls the associated task immediately rather than
// waiting for the next scheduled tick, closing the ADR-055 latency gap to
// the old in-memory gRPC waiter. *mcp.PollScheduler satisfies this
// structurally via its PollNow method (internal/mcp/tasks_scheduler.go) —
// declared here as an interface, rather than importing internal/mcp
// directly, so this file's dependency footprint stays exactly the DB +
// model surface AuthorizeActor actually needs.
//
// Nil-safe by design, per the doc comment on AuthorizeActorDeps.PollHint:
// who may act is a correctness question this method answers regardless;
// how fast the answer lands is a latency question the hint answers when
// it is wired, and a deployment that has not wired it yet still authorizes
// correctly.
type PollHint interface {
	PollNow(ctx context.Context, requestID string) error
}

// AuthorizeActorQuerier is the slice of the sqlc surface the AuthorizeActor
// handler needs directly (the actor directory carries its own, narrower,
// interface — see actordir.Querier).
type AuthorizeActorQuerier interface {
	GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error)
	InsertPluginAuditEvent(ctx context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error)

	// GetMCPServerByPluginInstance and GetMCPTaskByServerAndTaskID resolve
	// the caller's own Tasks-extension task id to the mcp_tasks row
	// PollHint.PollNow expects (issue #961 review item 6) — see
	// resolveTaskRowID's doc comment for why both steps, and why the second
	// one is scoped to the first.
	GetMCPServerByPluginInstance(ctx context.Context, pluginInstanceID *string) (db.McpServer, error)
	GetMCPTaskByServerAndTaskID(ctx context.Context, arg db.GetMCPTaskByServerAndTaskIDParams) (db.McpTask, error)
}

// AuthorizeActorDeps carries the collaborators host/authorize_actor needs.
type AuthorizeActorDeps struct {
	Querier AuthorizeActorQuerier

	// Directory resolves the caller-supplied actor_external_id, as the
	// calling instance names it, to a Gleipnir user and its roles. Required —
	// see the actordir package doc for the ADR-058 constraint on what may
	// back it.
	Directory actordir.Directory

	// PollHint is the poll-now signal, e.g. an *mcp.PollScheduler. Optional:
	// nil means authorization still happens correctly, it just resolves at
	// the ordinary poll cadence rather than immediately — the hint is
	// latency, not correctness (spec §6.4 Amendment 1).
	PollHint PollHint
}

// AuthorizeActorTools returns host/authorize_actor as a host-endpoint tool
// definition, ready for Server.Register.
func AuthorizeActorTools(deps AuthorizeActorDeps) []ToolDef {
	return []ToolDef{
		{
			Name: ToolAuthorizeActor,
			Description: "Authorize an externally-asserted actor identity against Gleipnir's admin-managed " +
				"user mapping before a channel lets it settle a pending HITL request, and hint the poll " +
				"scheduler to resolve the associated task immediately.",
			Handler: deps.authorizeActor,
		},
	}
}

// resolveInstance fetches the authenticated caller's instance row, mirroring
// Tier1Deps.resolveInstance (tier1.go) — duplicated rather than shared
// because the two Deps types intentionally carry disjoint Querier
// interfaces, and a shared helper would have to widen one of them to serve
// the other.
func (d AuthorizeActorDeps) resolveInstance(ctx context.Context) (db.PluginInstance, error) {
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

type authorizeActorArgs struct {
	RequestID       string `json:"request_id"`
	ActorExternalID string `json:"actor_external_id"`
}

// authorizeActor is the host/authorize_actor handler.
//
// request_id identifies the pending ask to the caller — the plugin's own
// Tasks-extension task id, the one value it holds at click time. This is
// NOT mcp_tasks.id (the host's own row identifier, what PollHint.PollNow
// actually expects — internal/mcp/tasks_scheduler.go's PollNow, GetTask, and
// Cancel all key on that row id, not the server's task_id), so it cannot be
// forwarded to PollHint verbatim: resolveTaskRowID translates it first,
// scoped to the calling instance's own mcp_servers row so one instance's
// task_id string can never resolve to a different instance's row. Settlement
// itself is still NOT this handler's job (see the package-level doc comment
// above) — only enough resolution to name the right row for the hint.
//
// An unauthorized actor is a NON-error result, mirroring the WriteAuditStep
// precedent exactly: the RPC succeeds, the result carries
// {"authorized": false}, a high-severity unauthorized_approval_attempt audit
// event is written, and nothing is resolved — the request is left open for
// a legitimately authorized actor (or the same one, corrected) to try again.
func (d AuthorizeActorDeps) authorizeActor(ctx context.Context, args json.RawMessage) (any, error) {
	inst, err := d.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}

	var a authorizeActorArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, &ToolError{Code: "invalid_argument", Message: fmt.Sprintf("decode arguments: %v", err)}
	}
	if a.RequestID == "" {
		return nil, &ToolError{Code: "invalid_argument", Message: "request_id is required"}
	}
	if a.ActorExternalID == "" {
		return nil, &ToolError{Code: "invalid_argument", Message: "actor_external_id is required"}
	}

	// Resolved against the calling instance: an actor id is only meaningful
	// together with the instance that issued it (internal/plugin/actordir).
	resolution, found, err := d.Directory.Resolve(ctx, inst.ID, a.ActorExternalID)
	if err != nil {
		return nil, &ToolError{Code: "internal", Message: fmt.Sprintf("resolve actor: %v", err)}
	}

	if !found || !hasAuthorizedRole(resolution.Roles) {
		d.writeAuditEvent(ctx, inst.ID, EventTypeUnauthorizedApproval, "high", map[string]string{
			"request_id":        a.RequestID,
			"actor_external_id": a.ActorExternalID,
			"tool":              ToolAuthorizeActor,
		})
		return map[string]any{"authorized": false}, nil
	}

	if d.PollHint != nil {
		rowID, resolveErr := d.resolveTaskRowID(ctx, inst.ID, a.RequestID)
		if resolveErr != nil {
			// Same best-effort posture as a failed PollNow below: the
			// request still resolves at the next scheduled poll tick.
			slog.WarnContext(ctx, "host/authorize_actor: resolving task row for poll-now hint failed",
				"request_id", a.RequestID, "instance", inst.ID, "err", resolveErr)
		} else if pollErr := d.PollHint.PollNow(ctx, rowID); pollErr != nil {
			// Best-effort: a failed hint must not turn a correct
			// authorization into a refusal — the request is still resolvable
			// at the next scheduled poll tick, so this is a latency
			// regression, not a correctness one.
			slog.WarnContext(ctx, "host/authorize_actor: poll-now hint failed",
				"request_id", a.RequestID, "row_id", rowID, "instance", inst.ID, "err", pollErr)
		}
	}

	return map[string]any{"authorized": true, "user_id": resolution.UserID}, nil
}

// resolveTaskRowID translates a plugin's own Tasks-extension task id into the
// mcp_tasks row PollHint.PollNow needs (issue #961 review item 6). Two steps,
// both required: GetMCPServerByPluginInstance names the CALLING instance's
// own mcp_servers row, and GetMCPTaskByServerAndTaskID looks up taskID
// scoped to exactly that row. Scoping to the caller's own server is not
// optional — mcp_tasks.task_id is only unique WITHIN one server
// (UNIQUE(server_id, task_id)), so looking it up unscoped could resolve to a
// different instance's task that happens to share the same server-assigned
// id string, letting one plugin's click hint at (and, via PollNow's poll,
// read the current state of) a task it does not own.
func (d AuthorizeActorDeps) resolveTaskRowID(ctx context.Context, instanceID, taskID string) (string, error) {
	srv, err := d.Querier.GetMCPServerByPluginInstance(ctx, &instanceID)
	if err != nil {
		return "", fmt.Errorf("resolve mcp server for instance %s: %w", instanceID, err)
	}
	task, err := d.Querier.GetMCPTaskByServerAndTaskID(ctx, db.GetMCPTaskByServerAndTaskIDParams{
		ServerID: &srv.ID,
		TaskID:   taskID,
	})
	if err != nil {
		return "", fmt.Errorf("resolve mcp_tasks row for server %s task %s: %w", srv.ID, taskID, err)
	}
	return task.ID, nil
}

// writeAuditEvent inserts a plugin_audit_events row, non-fatally: an audit
// insert failure is logged, never allowed to mask the rejection it records.
// Mirrors Tier1Deps.writeAuditEvent (tier1.go) for the same reason
// resolveInstance is duplicated above.
func (d AuthorizeActorDeps) writeAuditEvent(ctx context.Context, iid, eventType, severity string, payload map[string]string) {
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
