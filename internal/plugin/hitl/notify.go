package hitl

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
)

// This file is the Notify half of ADR-044's Notify/Request split, carried over
// the `io.gleipnir/channel` extension (spec §6.4) instead of v1.1's gRPC
// ChannelService. Nothing calls it yet — the actual fire happens where a HITL
// request is routed, which lives in the task adapters and #995's routing, both
// still to be built. NotifyRequestRouted is the seam those callers use.
//
// # What the message may never carry
//
// The trigger, per the owner's decision, is fixed: once a request is routed,
// every notify-enabled audience entry gets a host-authored "input requested"
// message naming the run and linking to Gleipnir's own run page — and NEVER
// the question itself. A tool-initiated ask's message is server-controlled
// content (spec §6.1); this is the one path that content could otherwise leak
// onto a chat channel Gleipnir does not control. NotifyRequestRouted builds
// the message from a RunRef alone, so there is no argument through which
// request text could arrive.
//
// # Why a failure here cannot matter to the run
//
// Notify is fire-and-forget by construction (ADR-044): the run already has its
// answer path (Request, or the in-app fallback) and does not read anything
// back from Notify. A per-entry failure is therefore recorded — an audit row
// and a metric — and never returned to a caller that could mistake it for a
// reason to fail the HITL flow that triggered it.

// NotifyClient is the narrow slice of an MCP client this file needs. *mcp.Client
// satisfies it.
type NotifyClient interface {
	ChannelCapabilityOf() (mcp.ChannelCapability, bool)
	ChannelNotify(ctx context.Context, n mcp.ChannelNotification) error
}

// NotifyClientResolver maps a plugin instance to its channel client for the
// Notify path. Separate from ClientResolver (the Request path's resolver)
// because the two return different narrow interfaces — a resolver that only
// implements one leg of the extension should not be forced to satisfy the
// other's method set.
type NotifyClientResolver interface {
	ChannelClientFor(instanceID string) (NotifyClient, error)
}

// CapabilityChecker answers whether an instance currently serves a capability.
// *caphealth.Registry satisfies it. Mirrors internal/plugin/events' interface
// of the same shape and purpose.
type CapabilityChecker interface {
	Serves(instanceID string, c caphealth.Capability) bool
}

// AuditWriter is the audit-table surface Notify needs. *db.Queries satisfies
// it.
type AuditWriter interface {
	InsertPluginAuditEvent(ctx context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error)
}

// NotifyFailureReason names why one audience entry's notify attempt did not
// deliver. Every reason here is survivable for the OTHER entries — Notify's
// whole point is that one bad channel must not silence the rest.
type NotifyFailureReason string

const (
	// NotifyFailureNoTarget — the entry's config does not name a delivery target.
	NotifyFailureNoTarget NotifyFailureReason = "no_delivery_target"

	// NotifyFailureChannelUnavailable — no reachable channel client for the entry.
	NotifyFailureChannelUnavailable NotifyFailureReason = "channel_unavailable"

	// NotifyFailureExtensionNotDeclared — the server does not do channels at all.
	NotifyFailureExtensionNotDeclared NotifyFailureReason = "channel_extension_not_declared"

	// NotifyFailureVersionUnsupported — the server declared a contract major
	// version this host cannot read. Mirrors the Request path's
	// SkipVersionUnsupported gate (route.go's majorVersionSupported).
	NotifyFailureVersionUnsupported NotifyFailureReason = "channel_version_unsupported"

	// NotifyFailureDeliveryUnsupported — the server does not support the
	// delivery target the entry is configured for.
	NotifyFailureDeliveryUnsupported NotifyFailureReason = "delivery_unsupported"

	// NotifyFailureCapabilityUnhealthy — caphealth is not currently serving
	// this instance's human_channel capability.
	NotifyFailureCapabilityUnhealthy NotifyFailureReason = "capability_unhealthy"

	// NotifyFailureRPCError — `channel/notify` itself errored.
	NotifyFailureRPCError NotifyFailureReason = "notify_rpc_failed"
)

// NotifyFailure is one entry that did not receive the notification, and why.
type NotifyFailure struct {
	EntryID    string
	InstanceID string
	Reason     NotifyFailureReason
	Detail     string
}

// NotifyReport summarizes one Notify fan-out. It is informational only — no
// caller may treat a non-empty Failed as a reason to fail anything, per
// ADR-044.
type NotifyReport struct {
	// Attempted is how many notify-enabled, non-in-app entries were tried.
	Attempted int
	Failed    []NotifyFailure
}

// notifyAuditEventType is the `plugin_audit_events.event_type` this file
// writes on a per-entry failure — the same string v1's dispatch.Dispatcher.Notify
// used, so the operational feed does not grow a second name for one fact.
const notifyAuditEventType = "notify_failed"

// defaultNotifyTimeout bounds a Notify fan-out's wall clock when the caller
// does not set one. Matches v1's dispatch.Dispatcher default.
const defaultNotifyTimeout = 10 * time.Second

// hitlNotifyFailuresTotal counts per-entry notify failures by reason. The
// reason set above is small and fixed, so this stays low-cardinality. Named
// gleipnir_plugin_* per ADR-047's plugin-metrics prefix convention, since every
// failure it counts is a plugin channel that did not deliver.
var hitlNotifyFailuresTotal = promauto.With(metrics.Registry()).NewCounterVec(
	prometheus.CounterOpts{
		Name: "gleipnir_plugin_hitl_notify_failures_total",
		Help: "Count of hitl audience notify attempts that failed to deliver, by reason.",
	},
	[]string{metrics.LabelReason},
)

// NotifyConfig wires a Notifier.
type NotifyConfig struct {
	Clients NotifyClientResolver
	// Health is optional: a nil checker skips the capability-health gate
	// entirely, which is the right default for a caller with no caphealth
	// registry to consult (mirrors events.Supervisor's nil-safe capability
	// field).
	Health CapabilityChecker
	// Audit is optional: a nil writer means per-entry failures are logged but
	// not persisted.
	Audit AuditWriter
	// Timeout bounds the whole fan-out's wall clock, not any single entry's —
	// but only the RPC leg (ChannelCapabilityOf + ChannelNotify). The audit
	// write that follows a failure runs on the caller's ctx, not the
	// timeout-bounded one, and is best-effort: it is not included in this
	// bound and a slow or failing audit write never re-opens it. Defaults to
	// defaultNotifyTimeout when zero.
	Timeout time.Duration
}

// Notifier fans a fixed message out to every notify-enabled audience entry
// over `io.gleipnir/channel`.
type Notifier struct {
	cfg NotifyConfig
}

// NewNotifier constructs a Notifier ready to use.
func NewNotifier(cfg NotifyConfig) *Notifier {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultNotifyTimeout
	}
	return &Notifier{cfg: cfg}
}

// Notify fans message out to every entry with Notify set, concurrently, bounded
// by one shared timeout. In-app entries are skipped — the SSE stream already
// covers the operator looking at this Gleipnir. Per-entry failures are
// recorded (audit row + metric) and never returned; the caller gets a report
// for its own logging, not a reason to change what it does next.
//
// runID is threaded onto every notify_failed audit row this call writes
// (plugin_audit_events.run_id is nullable for exactly this) — pass "" when
// there is no run to correlate against.
//
// Fan-out width is exactly the audience's entry count, which an admin already
// bounds when composing the audience (ADR-044) — this call does not multiply
// it.
func (n *Notifier) Notify(ctx context.Context, runID, message string, entries []Entry) NotifyReport {
	var targets []Entry
	for _, e := range entries {
		if e.Notify && !e.InApp {
			targets = append(targets, e)
		}
	}
	if len(targets) == 0 {
		return NotifyReport{}
	}

	notifyCtx, cancel := context.WithTimeout(ctx, n.cfg.Timeout)
	defer cancel()

	report := NotifyReport{Attempted: len(targets)}
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, entry := range targets {
		wg.Add(1)
		go func(entry Entry) {
			defer wg.Done()
			failure := n.notifyOne(notifyCtx, message, entry)
			if failure == nil {
				return
			}
			mu.Lock()
			report.Failed = append(report.Failed, *failure)
			mu.Unlock()
			hitlNotifyFailuresTotal.WithLabelValues(string(failure.Reason)).Inc()
			// The audit write uses the caller's ctx (not notifyCtx): a fan-out
			// that timed out should still get the chance to record why, rather
			// than losing the audit trail to the same deadline that produced it.
			// It is also best-effort past this point — Timeout bounds only the
			// RPC leg above, never the write itself.
			n.writeAuditFailure(ctx, runID, *failure)
		}(entry)
	}
	wg.Wait()
	return report
}

// notifyOne runs one entry's notify attempt. A non-nil return means "recorded
// as a failure"; the caller never propagates it further.
func (n *Notifier) notifyOne(ctx context.Context, message string, entry Entry) *NotifyFailure {
	fail := func(reason NotifyFailureReason, detail string) *NotifyFailure {
		return &NotifyFailure{EntryID: entry.EntryID, InstanceID: entry.InstanceID, Reason: reason, Detail: detail}
	}

	if entry.Target.Address == "" || !entry.Target.Delivery.Valid() {
		return fail(NotifyFailureNoTarget, "")
	}
	if n.cfg.Health != nil && !n.cfg.Health.Serves(entry.InstanceID, caphealth.Capability{Profile: caphealth.ProfileHumanChannel}) {
		return fail(NotifyFailureCapabilityUnhealthy, "")
	}
	if n.cfg.Clients == nil {
		return fail(NotifyFailureChannelUnavailable, "no notify client resolver configured")
	}
	client, err := n.cfg.Clients.ChannelClientFor(entry.InstanceID)
	if err != nil {
		return fail(NotifyFailureChannelUnavailable, err.Error())
	}

	capability, declared := client.ChannelCapabilityOf()
	if !declared {
		return fail(NotifyFailureExtensionNotDeclared, "")
	}
	if !majorVersionSupported(capability.Version) {
		return fail(NotifyFailureVersionUnsupported, capability.Version)
	}
	if !capability.Supports(entry.Target.Delivery) {
		return fail(NotifyFailureDeliveryUnsupported, string(entry.Target.Delivery))
	}

	if err := client.ChannelNotify(ctx, mcp.ChannelNotification{Target: entry.Target, Message: message}); err != nil {
		return fail(NotifyFailureRPCError, err.Error())
	}
	return nil
}

// writeAuditFailure records one notify failure as a plugin_audit_events row.
// Best-effort: a write failure is logged, not propagated — the Notify flow
// this failure came from has already finished by the time this runs.
func (n *Notifier) writeAuditFailure(ctx context.Context, runID string, failure NotifyFailure) {
	if n.cfg.Audit == nil {
		return
	}
	payload, err := json.Marshal(struct {
		EntryID string `json:"entry_id"`
		Reason  string `json:"reason"`
		Detail  string `json:"detail,omitempty"`
	}{EntryID: failure.EntryID, Reason: string(failure.Reason), Detail: failure.Detail})
	if err != nil {
		slog.Warn("hitl: marshal notify_failed audit payload", "entry_id", failure.EntryID, "err", err)
		return
	}

	params := db.InsertPluginAuditEventParams{
		EventType:   notifyAuditEventType,
		Severity:    "warning",
		PayloadJson: string(payload),
		CreatedAt:   timeNow().UTC().Format(time.RFC3339Nano),
	}
	if failure.InstanceID != "" {
		params.PluginInstanceID = &failure.InstanceID
	}
	if runID != "" {
		params.RunID = &runID
	}
	if _, err := n.cfg.Audit.InsertPluginAuditEvent(ctx, params); err != nil {
		slog.Warn("hitl: write notify_failed audit event", "entry_id", failure.EntryID, "err", err)
	}
}

// RunRef names the run a HITL request was routed for — just enough to build
// the fixed notify template, and deliberately nothing more. There is no field
// here a question or its arguments could travel through.
type RunRef struct {
	RunID      string
	PolicyName string
	// PublicURL is Gleipnir's own configured base URL (settings.Service's
	// system setting). Empty omits the link rather than guessing at an address
	// — e.g. localhost — that the person being notified may not be able to
	// reach.
	PublicURL string
}

// maxPolicyNameRunes bounds how much of a policy's name reaches the notify
// message. A policy name is operator-authored, not attacker-controlled like a
// tool-initiated ask, but it still travels into someone else's chat channel —
// so it gets the same defensive trim any third-party-bound text would.
const maxPolicyNameRunes = 128

// sanitizePolicyName strips control characters (including CR/LF, which could
// otherwise inject additional lines or formatting into a channel message) and
// truncates to maxPolicyNameRunes.
func sanitizePolicyName(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	if utf8.RuneCountInString(cleaned) <= maxPolicyNameRunes {
		return cleaned
	}
	runes := []rune(cleaned)
	return string(runes[:maxPolicyNameRunes])
}

// runLink is the run's own detail page under Gleipnir's public URL. It never
// points anywhere else: the notify message's one piece of dynamic content is
// a link into Gleipnir itself, never a plugin- or server-supplied address.
func (r RunRef) runLink() string {
	if r.PublicURL == "" || r.RunID == "" {
		return ""
	}
	return strings.TrimRight(r.PublicURL, "/") + "/runs/" + r.RunID
}

// NotifyRequestRouted fires the fixed "input requested" notification after a
// HITL request has been routed, to every notify-enabled entry in the audience
// that was routed. Callers fire this asynchronously — a notify failure, or the
// whole fan-out taking its full timeout, must never delay or fail the HITL
// flow that already committed to routing the request.
//
// The message is built ENTIRELY from run, never from the ask: this is the
// owner's decision (2026-09-24) that a server-authored question must never
// reach a notify-only channel, which by definition cannot enforce anything
// about what it forwards.
func NotifyRequestRouted(ctx context.Context, notifier *Notifier, entries []Entry, run RunRef) NotifyReport {
	if notifier == nil {
		return NotifyReport{}
	}

	message := fmt.Sprintf("Input requested for run %s", run.RunID)
	if policyName := sanitizePolicyName(run.PolicyName); policyName != "" {
		message = fmt.Sprintf("%s (%s)", message, policyName)
	}
	if link := run.runLink(); link != "" {
		message = fmt.Sprintf("%s: %s", message, link)
	}

	return notifier.Notify(ctx, run.RunID, message, entries)
}
