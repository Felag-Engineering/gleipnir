package hostendpoint

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
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

// ActorResolution is what an ActorDirectory found for one external actor id:
// the single Gleipnir user it is mapped to (admin-set today; ADR-058) and
// every role that user holds.
type ActorResolution struct {
	UserID   string
	Username string
	Roles    []model.Role
}

// ActorDirectory resolves an externally-asserted actor id (a Slack user.id
// or similar) to the Gleipnir user it names, if any.
//
// ADR-058 constraint: only a VERIFIED or admin-set identity link may feed
// actor authorization — a plugin's own claim about who clicked a button is
// not evidence on its own, and must not be trusted to assert a Gleipnir
// identity by itself. Until milestone #18 lands the `plugin_user_identities`
// verified-link table, DBActorDirectory below is the ONLY source, reading
// the admin-managed `users.slack_user_id` column: a mapping an operator
// typed into the admin UI, which is exactly the "admin-set" half of the
// ADR-058 bar, with no unverified path riding alongside it.
//
// This interface is the seam #18 widens: a verified plugin-attested link
// will fold in behind the same Resolve method, keyed off its own
// verified_at, without AuthorizeActor's handler (below) changing at all —
// the widening is in what backs ActorDirectory, not in how the handler uses
// it.
type ActorDirectory interface {
	// Resolve looks up actorExternalID. found=false means no admin-set
	// mapping exists (an unknown external id, or a mapped user with zero
	// granted roles) — the caller treats that identically to "mapped but
	// unauthorized," per the S6 precedent in hostsvc.
	Resolve(ctx context.Context, actorExternalID string) (resolution ActorResolution, found bool, err error)
}

// DBActorDirectoryQuerier is the sqlc surface DBActorDirectory needs.
// *db.Queries satisfies it.
type DBActorDirectoryQuerier interface {
	GetUserBySlackUserID(ctx context.Context, slackUserID *string) ([]db.GetUserBySlackUserIDRow, error)
}

// DBActorDirectory is the Tier-1 ActorDirectory: the admin-managed
// `users.slack_user_id` mapping, joined to `user_roles`, deactivated users
// excluded by the query itself (see GetUserBySlackUserID's SQL). This is the
// ADR-058-compliant source available today; see ActorDirectory's doc comment
// for what #18 adds alongside it.
type DBActorDirectory struct {
	Querier DBActorDirectoryQuerier
}

// Resolve implements ActorDirectory.
func (d DBActorDirectory) Resolve(ctx context.Context, actorExternalID string) (ActorResolution, bool, error) {
	rows, err := d.Querier.GetUserBySlackUserID(ctx, &actorExternalID)
	if err != nil {
		return ActorResolution{}, false, fmt.Errorf("look up actor external id: %w", err)
	}
	if len(rows) == 0 {
		// Unknown external id, or a mapped user with no roles at all — the
		// query's JOIN against user_roles already collapses "no roles" into
		// "no rows" (see the SQL comment in queries/users.sql).
		return ActorResolution{}, false, nil
	}
	res := ActorResolution{UserID: rows[0].ID, Username: rows[0].Username}
	for _, row := range rows {
		res.Roles = append(res.Roles, model.Role(row.Role))
	}
	return res, true, nil
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
// handler needs directly (ActorDirectory carries its own, narrower,
// interface — see DBActorDirectoryQuerier above).
type AuthorizeActorQuerier interface {
	GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error)
	InsertPluginAuditEvent(ctx context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error)
}

// AuthorizeActorDeps carries the collaborators host/authorize_actor needs.
type AuthorizeActorDeps struct {
	Querier AuthorizeActorQuerier

	// Directory resolves the caller-supplied actor_external_id to a Gleipnir
	// user and its roles. Required — see ActorDirectory's doc comment for
	// the ADR-058 constraint on what may back it.
	Directory ActorDirectory

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
// Tasks-extension task id, the one value it holds at click time — and is
// forwarded to PollHint verbatim. Resolving it to a concrete mcp_tasks row
// is deliberately NOT this handler's job (see the package-level doc comment
// above): AuthorizeActor authorizes the actor and hints the poll, and
// settlement — including whatever identifier translation the poll-now path
// needs — stays with the task/CAS machinery on the other side of PollHint.
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

	resolution, found, err := d.Directory.Resolve(ctx, a.ActorExternalID)
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
		if pollErr := d.PollHint.PollNow(ctx, a.RequestID); pollErr != nil {
			// Best-effort: a failed hint must not turn a correct
			// authorization into a refusal — the request is still resolvable
			// at the next scheduled poll tick, so this is a latency
			// regression, not a correctness one.
			slog.WarnContext(ctx, "host/authorize_actor: poll-now hint failed",
				"request_id", a.RequestID, "instance", inst.ID, "err", pollErr)
		}
	}

	return map[string]any{"authorized": true, "user_id": resolution.UserID}, nil
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
