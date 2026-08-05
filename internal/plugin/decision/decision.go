// Package decision records what a human decided when a tool asked them
// something mid-execution (ADR-055 / ADR-046, mcp-realignment-spec.md §6.6).
//
// # Why these are not step types
//
// The spec names two additions — `tool_permission_request` and
// `tool_input_request` — "alongside the existing `approval_request` /
// `feedback_request`". The analogy is to the shape of those records, not to
// where they live. These two are deliberately NOT `model.StepType` values, and
// a test in this package pins that.
//
// ADR-046 splits the two tables on one line: `run_steps` is replayed into the
// model's context, so anything written there is something the agent reads.
// The agent-visible trace of a paused-and-resumed call is `tool_call` →
// `tool_result` and nothing else. Adding these to `StepType` would make them
// eligible for that replay by construction, and the purity test would then be
// guarding a door already standing open. Worse, it would hand a server a route
// for smuggling text into the model's context through an operator's own answer.
//
// So they go to `plugin_audit_events`, which is the operational record: never
// replayed, never summarized into a prompt, and readable by an auditor.
//
// # Why the record is this wide
//
// "An approval happened" is not oversight evidence. Article-14-grade evidence
// answers who, through what, how strongly that channel knows them, what they
// were asked, by when, and what came of it. Each field here exists because its
// absence would let two materially different events produce identical records:
//
//   - Assurance without an actor says a strong channel was used but not by whom.
//   - An actor without a LinkMethod is a name the channel asserted; whether the
//     host could tie it to a Gleipnir user is the difference between evidence
//     and hearsay.
//   - Classification separates "someone consented" from "someone typed a value",
//     which carry different authority and are answerable by different roles.
//   - The considered-and-skipped list is why THIS channel: a decision made by the
//     third entry in an audience only means something alongside why the first two
//     did not make it.
//   - Outcome distinguishes a refusal from a silence. Both leave the request
//     unfulfilled; only one of them involved a human.
//
// # One record per request
//
// Exactly one decision record is written per request, at settlement, carrying
// the outcome. The request itself is already durable in `tool_input_requests`,
// so a separate "we asked" row would add nothing an auditor cannot already see
// — while a second row per request would double-count approvals in any export
// that counts them.
package decision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// RecordType is the `plugin_audit_events.event_type` value of a decision
// record. There are two because the §6.1 classification is the first thing an
// auditor filters on: consent carries different authority from data entry.
type RecordType string

const (
	// RecordToolPermissionRequest is a consent-only ask — approve or reject.
	RecordToolPermissionRequest RecordType = "tool_permission_request"

	// RecordToolInputRequest is a request for values the server needs.
	RecordToolInputRequest RecordType = "tool_input_request"
)

func (t RecordType) Valid() bool {
	switch t {
	case RecordToolPermissionRequest, RecordToolInputRequest:
		return true
	}
	return false
}

// RecordTypeFor maps the elicitation classification onto the record type, so
// the two vocabularies cannot drift apart at a call site.
func RecordTypeFor(kind model.ElicitationKind) RecordType {
	if kind == model.ElicitationKindInformation {
		return RecordToolInputRequest
	}
	return RecordToolPermissionRequest
}

// Outcome is how a request ended.
type Outcome string

const (
	// OutcomeAnswered — a human accepted, or supplied the values asked for.
	OutcomeAnswered Outcome = "answered"

	// OutcomeRejected — a human declined. This is a DECISION, not a failure:
	// MRTR hands the refusal back to the server, which decides what to do with
	// it. Recording it as an outcome distinct from a timeout is the whole
	// difference between "someone said no" and "nobody was there".
	OutcomeRejected Outcome = "rejected"

	// OutcomeTimeout — the effective deadline passed with no answer.
	OutcomeTimeout Outcome = "timeout"

	// OutcomeCancelled — the run or the request was terminated before anyone
	// answered.
	OutcomeCancelled Outcome = "cancelled"

	// OutcomeReplayedAfterTTL — the server discarded its MRTR state, re-asked
	// the identical question, and the host spent the answer already in hand
	// without troubling the human again (spec §6.5).
	//
	// It is its own outcome rather than a second "answered" because no human
	// acted at this moment. Reading it as a fresh approval would be reading one
	// consent as two.
	OutcomeReplayedAfterTTL Outcome = "replayed_after_ttl"
)

func (o Outcome) Valid() bool {
	switch o {
	case OutcomeAnswered, OutcomeRejected, OutcomeTimeout, OutcomeCancelled, OutcomeReplayedAfterTTL:
		return true
	}
	return false
}

// HadActor reports whether a human acted at this moment. Timeouts, cancels,
// and replays did not, and a record claiming an actor for one of them is
// self-contradictory.
func (o Outcome) HadActor() bool {
	return o == OutcomeAnswered || o == OutcomeRejected
}

// LinkMethod is how the host established that the acting external identity
// belongs to a known Gleipnir user.
//
// The field exists because an actor ID on its own is a claim made by the
// channel, and how much it is worth is exactly what Assurance measures. Storing
// the method rather than a bare boolean means a later change of policy — say,
// deciding directory mappings are no longer sufficient for permission — can be
// applied to historical records instead of being unanswerable about them.
type LinkMethod string

const (
	// LinkSession — the actor authenticated to THIS host. The in-app channel's
	// case, and the strongest link available: no third party is being trusted.
	LinkSession LinkMethod = "session"

	// LinkDirectory — an external ID resolved through the admin-managed user
	// mapping (the `users.slack_user_id` shape). The mapping is an operator's
	// assertion, verified once at configuration time rather than per action.
	LinkDirectory LinkMethod = "directory_mapping"

	// LinkUnverified — the channel named someone the host could not tie to any
	// Gleipnir user. The record keeps the name; it must not be read as identity.
	LinkUnverified LinkMethod = "unverified"

	// LinkNone — nobody acted, so there is nothing to link.
	LinkNone LinkMethod = "none"
)

func (m LinkMethod) Valid() bool {
	switch m {
	case LinkSession, LinkDirectory, LinkUnverified, LinkNone:
		return true
	}
	return false
}

// Verified reports whether the acting identity is tied to a Gleipnir user.
func (m LinkMethod) Verified() bool {
	return m == LinkSession || m == LinkDirectory
}

// Candidate is one audience entry that was considered and passed over, with
// the router's reason. Held as plain strings so this package does not depend on
// the router's vocabulary: the record is evidence, and evidence that only one
// producer can write is a record with a hidden coupling.
type Candidate struct {
	EntryID    string `json:"entry_id"`
	InstanceID string `json:"instance_id,omitempty"`
	Reason     string `json:"reason"`
}

// Record is one settled tool-initiated request.
type Record struct {
	// RunID and RequestID identify what this decision was about. RequestID is
	// the `tool_input_requests` row, and is also the exactly-once key.
	RunID     string `json:"-"`
	RequestID string `json:"request_id"`

	// Kind is the §6.1 classification. It decides the record type.
	Kind model.ElicitationKind `json:"kind"`

	// ToolName is the granted tool whose call paused, as the agent called it.
	ToolName string `json:"tool_name,omitempty"`

	// Channel identifies who was asked. EntryID names the audience entry;
	// InstanceID is the plugin behind it, empty for the in-app channel.
	ChannelEntryID   string `json:"channel_entry_id"`
	ChannelInstance  string `json:"channel_instance_id,omitempty"`
	ChannelAssurance string `json:"channel_assurance"`

	// ActorExternalID is the channel's identifier for the person who acted.
	// Untrusted on its own — see LinkMethod.
	ActorExternalID string `json:"actor_external_id,omitempty"`

	// ActorUserID is the Gleipnir user the actor was resolved to, when the link
	// could be established. It is also written to the indexed
	// `actor_user_id` column so "everything user X approved" is a query rather
	// than a JSON scan.
	ActorUserID string `json:"actor_user_id,omitempty"`

	// LinkMethod is how ActorUserID was established.
	LinkMethod LinkMethod `json:"link_method"`

	// EffectiveDeadline is the deadline that actually governed the wait — the
	// minimum of the policy timeout, the server task TTL, and any requestState
	// TTL (spec §6.3). The MINIMUM is what an operator saw and what expiry was
	// judged against, so recording any one clock alone would misstate why a
	// request ran out of time.
	EffectiveDeadline time.Time `json:"effective_deadline,omitzero"`

	// DeadlineSource names which clock won.
	DeadlineSource string `json:"deadline_source,omitempty"`

	// Outcome is how it ended.
	Outcome Outcome `json:"outcome"`

	// Considered is every audience entry passed over before the chosen one, in
	// order.
	Considered []Candidate `json:"considered,omitempty"`

	// DecidedAt is when the outcome was reached.
	DecidedAt time.Time `json:"decided_at"`
}

// Validate rejects a record that would be misleading to read back. The checks
// are the self-contradictions: an outcome that names an actor who did not act,
// or a verified link with nobody linked.
func (r Record) Validate() error {
	if r.RunID == "" {
		return fmt.Errorf("decision record: run ID is required")
	}
	if r.RequestID == "" {
		return fmt.Errorf("decision record: request ID is required")
	}
	if !r.Kind.Valid() {
		return fmt.Errorf("decision record: elicitation kind %q is not %q or %q",
			r.Kind, model.ElicitationKindPermission, model.ElicitationKindInformation)
	}
	if !r.Outcome.Valid() {
		return fmt.Errorf("decision record: outcome %q is not a known outcome", r.Outcome)
	}
	if !r.LinkMethod.Valid() {
		return fmt.Errorf("decision record: link method %q is not a known method", r.LinkMethod)
	}
	if !r.Outcome.HadActor() && (r.ActorExternalID != "" || r.ActorUserID != "") {
		return fmt.Errorf("decision record: outcome %q names an actor, but nobody acted", r.Outcome)
	}
	if r.LinkMethod.Verified() && r.ActorUserID == "" {
		return fmt.Errorf("decision record: link method %q claims a verified user, but none is named", r.LinkMethod)
	}
	if !r.LinkMethod.Verified() && r.ActorUserID != "" {
		return fmt.Errorf("decision record: names a Gleipnir user but link method is %q", r.LinkMethod)
	}
	return nil
}

// Type is the record's `event_type`.
func (r Record) Type() RecordType { return RecordTypeFor(r.Kind) }

// Severity ranks the record for the operational feed.
//
// A permission granted by an actor the host could not link to a user is the one
// case raised above `info`: it is the shape a forged approval takes, and an
// operator scanning the feed should not have to open every record to find it.
// Timeouts and cancels are `warning` because an unanswered request is a run
// that stalled on people, which is worth noticing without being an incident.
func (r Record) Severity() string {
	switch r.Outcome {
	case OutcomeTimeout, OutcomeCancelled:
		return "warning"
	case OutcomeAnswered, OutcomeRejected:
		if r.Kind == model.ElicitationKindPermission && !r.LinkMethod.Verified() {
			return "warning"
		}
	}
	return "info"
}

// AssuranceOf renders a channel assurance for the record, mapping the unknown
// value to an explicit marker rather than an empty string. "The server declared
// something this host does not recognize" and "no assurance was recorded" are
// different facts, and an empty field cannot say which.
func AssuranceOf(a mcp.ChannelAssurance) string {
	if a.Valid() {
		return string(a)
	}
	if a == "" {
		return "undeclared"
	}
	return "unrecognized"
}

// ErrAlreadyRecorded reports that this request already has a decision record.
var ErrAlreadyRecorded = errors.New("decision record already written for this request")

// Store is the audit-table surface this package needs. *db.Queries satisfies it.
type Store interface {
	InsertPluginAuditEvent(ctx context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error)
	ListPluginAuditEventsByRun(ctx context.Context, runID *string) ([]db.PluginAuditEvent, error)
}

// Recorder writes decision records.
type Recorder struct {
	store Store

	mu      sync.Mutex
	settled map[string]struct{}
}

func NewRecorder(store Store) *Recorder {
	return &Recorder{store: store, settled: make(map[string]struct{})}
}

// timeNow is the package's injectable clock (CLAUDE.md "Testing time-dependent
// code"). Tests swap it via t.Cleanup and must not call t.Parallel().
var timeNow = func() time.Time { return time.Now() }

// Record writes one decision record, at most once per request.
//
// The duplicate guard is a claim taken BEFORE the insert and released if the
// insert fails: taking it after would let two concurrent settlements both write,
// and releasing it on success would let a retry write a second. What it protects
// against is a settlement path that runs twice inside one process — a retry
// after a partial failure, or two goroutines racing to finalize the same wait.
//
// It is not the durable guarantee. That comes from the caller: a request reaches
// exactly one terminal state, arbitrated by the run-state CAS (ADR-038) or the
// task row's, and only that transition's winner calls this. The map is a
// backstop, not the mechanism.
func (r *Recorder) Record(ctx context.Context, rec Record) error {
	if err := rec.Validate(); err != nil {
		return err
	}
	if rec.DecidedAt.IsZero() {
		rec.DecidedAt = timeNow().UTC()
	}
	if !r.claim(rec.RequestID) {
		return fmt.Errorf("%w: request %s", ErrAlreadyRecorded, rec.RequestID)
	}

	payload, err := json.Marshal(rec)
	if err != nil {
		r.release(rec.RequestID)
		return fmt.Errorf("marshal decision record for request %s: %w", rec.RequestID, err)
	}

	params := db.InsertPluginAuditEventParams{
		EventType:   string(rec.Type()),
		Severity:    rec.Severity(),
		PayloadJson: string(payload),
		CreatedAt:   rec.DecidedAt.UTC().Format(time.RFC3339Nano),
		RunID:       &rec.RunID,
	}
	if rec.ChannelInstance != "" {
		params.PluginInstanceID = &rec.ChannelInstance
	}
	if rec.ActorUserID != "" {
		params.ActorUserID = &rec.ActorUserID
	}

	if _, err := r.store.InsertPluginAuditEvent(ctx, params); err != nil {
		r.release(rec.RequestID)
		return fmt.Errorf("write decision record for request %s: %w", rec.RequestID, err)
	}
	return nil
}

// ForRun returns the run's decision records in the order they were decided.
//
// Rows that are not decision records are skipped rather than surfaced: the run
// column is on the shared audit table, and a caller asking for decisions should
// not have to filter out whatever else grows a run association later.
func (r *Recorder) ForRun(ctx context.Context, runID string) ([]Record, error) {
	rows, err := r.store.ListPluginAuditEventsByRun(ctx, &runID)
	if err != nil {
		return nil, fmt.Errorf("list decision records for run %s: %w", runID, err)
	}
	out := make([]Record, 0, len(rows))
	for _, row := range rows {
		rec, ok := Decode(row)
		if !ok {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// Decode reads a decision record back out of an audit row. The second return
// is false for a row that is not a decision record, or whose payload does not
// parse — an unreadable record must never be handed back as an empty one, since
// an empty record reads as "nobody acted".
func Decode(row db.PluginAuditEvent) (Record, bool) {
	if !RecordType(row.EventType).Valid() {
		return Record{}, false
	}
	var rec Record
	if err := json.Unmarshal([]byte(row.PayloadJson), &rec); err != nil {
		return Record{}, false
	}
	if row.RunID != nil {
		rec.RunID = *row.RunID
	}
	return rec, true
}

func (r *Recorder) claim(requestID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, taken := r.settled[requestID]; taken {
		return false
	}
	r.settled[requestID] = struct{}{}
	return true
}

func (r *Recorder) release(requestID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.settled, requestID)
}
