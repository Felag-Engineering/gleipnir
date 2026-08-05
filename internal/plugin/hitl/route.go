// Package hitl routes ONE tool-initiated human-in-the-loop request across a
// policy's audience (ADR-055 / ADR-044, mcp-realignment-spec.md §4.1 and §6).
//
// A tool-initiated request is asked of exactly one channel. The audience is an
// ordered list of candidates, not a fan-out: Notify broadcasts, Request picks.
// This package is the picking, and it adds one rule the v1.1 audience did not
// have — the HOST decides which request kinds a channel may settle, from the
// assurance level that channel declares.
//
// The asymmetry is the whole point. A weak channel may supply *information*,
// because a wrong answer there is an answer the agent then acts on visibly. It
// may not grant *permission*, because a forged approval is indistinguishable
// from a real one after the fact — and a decision record that cannot tell them
// apart is not oversight evidence. A weak channel does not fail the request; it
// is skipped, and the next entry is tried.
//
// Enforcement is host-side, before `channel/request` is ever issued. The kind
// is still sent to the channel so it can render a permission prompt differently
// from a form, but a rule enforced by the party it constrains is not a rule.
//
// # Where the answer comes back
//
// Nowhere in this package. Routing ends when the ask is durable and addressable:
// a row in `mcp_tasks`, kind `channel_request`, carrying the task handle. From
// there the two routes diverge exactly as far as they must — a plugin-routed
// task is polled by `internal/mcp`'s PollScheduler, an in-app task is delivered
// by function call (`inapptask.Manager.Await`). Both end at the same terminal
// row, and Complete is the one place either of them can be settled.
//
// Resolution latency for a plugin-routed ask is therefore bounded by the poll
// interval. The spec closes that gap with poll-on-signal — the plugin's
// click-time `AuthorizeActor` callback doubles as a PollNow hint (§6.4,
// Amendment 1) — which needs the host endpoint that does not exist yet
// (milestone #17). Until it lands, the interval is the bound.
package hitl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/inapptask"
)

// InAppAssurance is the assurance level of the built-in `gleipnir.in-app`
// channel, and it is the strongest one in the system: the person answering
// authenticated to THIS host, not to a third party whose identity claims the
// host has to take on faith. That is why the in-app entry settles both kinds
// and why it makes a sound last resort for a permission request.
const InAppAssurance = mcp.ChannelAssuranceAuthenticated

// SkipReason names why an audience entry did not receive the request. Every
// skip is recorded rather than logged and forgotten: "the third channel
// answered" is only meaningful evidence alongside why the first two did not,
// and the audit decision record (spec §6.6) is where that pair belongs.
type SkipReason string

const (
	// SkipNotRequestCapable — the entry is configured for Notify only.
	SkipNotRequestCapable SkipReason = "not_request_capable"

	// SkipNoTarget — the entry's config does not name a delivery target.
	SkipNoTarget SkipReason = "no_delivery_target"

	// SkipChannelUnavailable — no reachable channel client for the entry.
	SkipChannelUnavailable SkipReason = "channel_unavailable"

	// SkipExtensionNotDeclared — the server does not do channels at all.
	SkipExtensionNotDeclared SkipReason = "channel_extension_not_declared"

	// SkipVersionUnsupported — the server declared a contract major version
	// this host cannot read.
	SkipVersionUnsupported SkipReason = "channel_version_unsupported"

	// SkipAssuranceTooWeak — the §4.1 gate: this channel may not settle this
	// kind of request.
	SkipAssuranceTooWeak SkipReason = "assurance_too_weak"

	// SkipDeliveryUnsupported — the server does not support the delivery target
	// the entry is configured for.
	SkipDeliveryUnsupported SkipReason = "delivery_unsupported"

	// SkipRequestFailed — `channel/request` errored, so nothing was asked.
	SkipRequestFailed SkipReason = "channel_request_failed"

	// SkipTaskNotPersisted — the ask reached the channel but its task row could
	// not be written.
	SkipTaskNotPersisted SkipReason = "task_not_persisted"
)

// ErrNoEligibleEntry reports that every entry in the audience was skipped and
// no in-app fallback was available to catch the request.
//
// This is only reachable when an operator disabled the in-app fallback, since
// the synthetic entry is authenticated and settles both kinds. That makes it a
// configuration fault rather than a runtime one, and it is worth failing loudly:
// the alternative is a request nobody is ever asked, waiting out its deadline
// as if a human had ignored it.
var ErrNoEligibleEntry = errors.New("no audience entry may settle this request")

// Skip is one entry that did not receive the request, and why.
type Skip struct {
	EntryID    string
	InstanceID string
	Reason     SkipReason

	// Detail carries the underlying error text when there is one. Empty for a
	// structural skip like a weak channel, where the reason says everything.
	Detail string
}

// Entry is one audience candidate as the router sees it.
//
// It is deliberately flat data rather than a DB row: the routing decision is a
// pure function of (ordered entries, kind, what each channel declares), and
// keeping it that way is what lets the whole fall-through table be tested
// without a database or a live MCP server.
type Entry struct {
	// EntryID identifies the audience entry (`gleipnir.in-app` for the
	// synthetic one).
	EntryID string

	// InstanceID is the plugin instance behind this entry. Empty for in-app.
	InstanceID string

	// ServerID is the `mcp_servers` row backing this entry's channel, stored on
	// the task so the poller knows who to ask. Empty for in-app, which is the
	// NULL that means "resolved internally" (spec §6.4).
	ServerID string

	// InApp marks the synthetic `gleipnir.in-app` entry.
	InApp bool

	// Request mirrors the audience entry's request capability flag.
	Request bool

	// Target is where the message lands. Unused for in-app, which has exactly
	// one destination: the operator looking at this Gleipnir.
	Target mcp.ChannelTarget
}

// Ask is what is being put to a human.
type Ask struct {
	RunID string

	// Message is what the human is asked. For a tool-initiated request this is
	// SERVER-controlled text (spec §6.1) — every renderer downstream must treat
	// it as untrusted content, not as markup and not as instructions.
	Message string

	// Options are the choices offered, for a pick-one ask.
	Options []inapptask.Option

	// RequestedSchema is the form, for a typed-values ask.
	RequestedSchema json.RawMessage

	// Kind decides which channels may settle it (§4.1) and which role may
	// answer it in-app (model.ElicitationKind.RequiredRole).
	Kind model.ElicitationKind
}

// Routed is where an ask landed.
type Routed struct {
	// EntryID / InstanceID identify the chosen audience entry.
	EntryID    string
	InstanceID string

	// InApp is true when the ask fell to the built-in channel.
	InApp bool

	// RowID is the `mcp_tasks` primary key. This is the handle Complete takes,
	// for both routes.
	RowID string

	// TaskID is the task identifier — the server's for a plugin-routed ask, a
	// host-minted ULID for an in-app one.
	TaskID string

	// Assurance is what the chosen channel claimed. Stored on the decision
	// record so an approval can be read as evidence of a known strength rather
	// than as a bare assertion that someone approved (spec §6.6).
	Assurance mcp.ChannelAssurance

	// PollInterval is the server's suggested cadence, zero for in-app.
	PollInterval time.Duration

	// Skipped is every entry passed over to get here, in audience order.
	Skipped []Skip
}

// ChannelClient is the narrow slice of an MCP client this package needs.
// *mcp.Client satisfies it.
type ChannelClient interface {
	ChannelCapabilityOf() (mcp.ChannelCapability, bool)
	ChannelRequest(ctx context.Context, p mcp.ChannelRequestParams) (mcp.TaskStatus, error)
}

// ClientResolver maps a plugin instance to its channel client. A resolver that
// returns an error for an instance makes it a skip, not a failure — an audience
// exists precisely so one unreachable channel is survivable.
type ClientResolver interface {
	ChannelClientFor(instanceID string) (ChannelClient, error)
}

// InAppOpener opens the built-in channel's leg. *inapptask.Manager satisfies it.
type InAppOpener interface {
	Open(ctx context.Context, req inapptask.OpenRequest) (inapptask.TaskHandle, error)
}

// Completer settles a task row. *inapptask.Manager satisfies it.
type Completer interface {
	Complete(ctx context.Context, rowID string, r inapptask.Resolution) error
}

// TaskStore is the task-table surface needed to make a plugin-routed ask
// durable. *db.Queries satisfies it.
type TaskStore interface {
	CreateMCPTask(ctx context.Context, arg db.CreateMCPTaskParams) (db.McpTask, error)
}

// Config wires a Router.
type Config struct {
	Clients   ClientResolver
	InApp     InAppOpener
	Completer Completer
	Tasks     TaskStore
}

// Router picks the audience entry that will be asked, and settles the answer.
type Router struct {
	clients   ClientResolver
	inApp     InAppOpener
	completer Completer
	tasks     TaskStore
}

// timeNow is the package's injectable clock (CLAUDE.md "Testing time-dependent
// code"). Tests swap it via t.Cleanup and must not call t.Parallel().
var timeNow = func() time.Time { return time.Now() }

func New(cfg Config) (*Router, error) {
	if cfg.InApp == nil {
		return nil, fmt.Errorf("hitl: InApp opener is required")
	}
	if cfg.Completer == nil {
		return nil, fmt.Errorf("hitl: Completer is required")
	}
	if cfg.Tasks == nil {
		return nil, fmt.Errorf("hitl: Tasks store is required")
	}
	// Clients may be nil: an instance with no plugin channels configured routes
	// every request in-app, and requiring a resolver it would never call would
	// make the common case carry the uncommon one's wiring.
	return &Router{
		clients:   cfg.Clients,
		inApp:     cfg.InApp,
		completer: cfg.Completer,
		tasks:     cfg.Tasks,
	}, nil
}

// Route asks the first eligible entry and returns where the ask landed.
//
// Entries are walked in audience order and the FIRST one that can settle this
// kind of request gets it. Nothing is asked twice: an audience is a priority
// list, and asking two channels the same question would put two humans in a
// race whose loser is told their decision did not count.
func (r *Router) Route(ctx context.Context, ask Ask, entries []Entry) (Routed, error) {
	if err := validateAsk(ask); err != nil {
		return Routed{}, err
	}

	var skipped []Skip
	for _, entry := range entries {
		if !entry.Request {
			skipped = append(skipped, Skip{
				EntryID: entry.EntryID, InstanceID: entry.InstanceID,
				Reason: SkipNotRequestCapable,
			})
			continue
		}

		if entry.InApp {
			routed, err := r.openInApp(ctx, ask, entry)
			if err != nil {
				// The in-app leg failing is not a fall-through candidate: it is
				// the last entry by construction, and its failure is a host
				// fault rather than a channel being unsuitable.
				return Routed{}, err
			}
			routed.Skipped = skipped
			return routed, nil
		}

		routed, skip := r.requestFromChannel(ctx, ask, entry)
		if skip != nil {
			skipped = append(skipped, *skip)
			continue
		}
		routed.Skipped = skipped
		return routed, nil
	}

	return Routed{Skipped: skipped}, fmt.Errorf("%w: %d entr%s considered",
		ErrNoEligibleEntry, len(entries), plural(len(entries)))
}

// requestFromChannel runs the §4.1 gate and, if it passes, issues the ask.
// Returning a non-nil *Skip means "try the next entry" — every rejection on
// this path is survivable, because none of them delivered anything.
func (r *Router) requestFromChannel(ctx context.Context, ask Ask, entry Entry) (Routed, *Skip) {
	skip := func(reason SkipReason, detail string) *Skip {
		return &Skip{EntryID: entry.EntryID, InstanceID: entry.InstanceID, Reason: reason, Detail: detail}
	}

	if entry.Target.Address == "" || !entry.Target.Delivery.Valid() {
		return Routed{}, skip(SkipNoTarget, "")
	}
	if r.clients == nil {
		return Routed{}, skip(SkipChannelUnavailable, "no channel client resolver configured")
	}
	client, err := r.clients.ChannelClientFor(entry.InstanceID)
	if err != nil {
		return Routed{}, skip(SkipChannelUnavailable, err.Error())
	}

	capability, declared := client.ChannelCapabilityOf()
	if !declared {
		return Routed{}, skip(SkipExtensionNotDeclared, "")
	}
	if !majorVersionSupported(capability.Version) {
		return Routed{}, skip(SkipVersionUnsupported, capability.Version)
	}
	// The §4.1 gate. Note it runs on the DECLARED assurance, including the zero
	// value a malformed declaration decodes to — which resolves nothing, so an
	// unreadable declaration is skipped rather than guessed upward.
	if !capability.Assurance.MayResolve(mcp.ElicitationKind(ask.Kind)) {
		return Routed{}, skip(SkipAssuranceTooWeak, string(capability.Assurance))
	}
	if !capability.Supports(entry.Target.Delivery) {
		return Routed{}, skip(SkipDeliveryUnsupported, string(entry.Target.Delivery))
	}

	status, err := client.ChannelRequest(ctx, mcp.ChannelRequestParams{
		Target:          entry.Target,
		Message:         ask.Message,
		RequestedSchema: ask.RequestedSchema,
		Options:         toChannelOptions(ask.Options),
		Kind:            mcp.ElicitationKind(ask.Kind),
	})
	if err != nil {
		// A failed request asked nobody anything, so falling through is safe
		// and is the behavior the extension contract expects — a notify-only
		// channel that declares the extension errors here, and the audience
		// moves on (extension doc §11, case C).
		return Routed{}, skip(SkipRequestFailed, err.Error())
	}

	rowID, err := r.persistTask(ctx, ask.RunID, entry.ServerID, status)
	if err != nil {
		// The ask is live on the channel but unaddressable from here: no row
		// means nothing polls it and nothing can cancel it. Skipping to the
		// next entry is the recoverable direction — a human may see two asks,
		// but Complete arbitrates, and the alternative is a wait that can only
		// end in a timeout.
		return Routed{}, skip(SkipTaskNotPersisted, err.Error())
	}

	return Routed{
		EntryID:      entry.EntryID,
		InstanceID:   entry.InstanceID,
		RowID:        rowID,
		TaskID:       status.TaskID,
		Assurance:    capability.Assurance,
		PollInterval: status.PollInterval,
	}, nil
}

// openInApp routes to the built-in channel.
func (r *Router) openInApp(ctx context.Context, ask Ask, entry Entry) (Routed, error) {
	handle, err := r.inApp.Open(ctx, inapptask.OpenRequest{
		RunID:           ask.RunID,
		Message:         ask.Message,
		Options:         ask.Options,
		RequestedSchema: ask.RequestedSchema,
		Kind:            ask.Kind,
	})
	if err != nil {
		return Routed{}, fmt.Errorf("routing to the in-app channel: %w", err)
	}
	return Routed{
		EntryID:   entry.EntryID,
		InApp:     true,
		RowID:     handle.ID,
		TaskID:    handle.TaskID,
		Assurance: InAppAssurance,
	}, nil
}

// persistTask writes the durable row for a plugin-routed ask. Same table, same
// kind, same terminal vocabulary as the in-app leg — that sameness is what
// makes one resolution path and one audit shape possible (spec §6.4).
func (r *Router) persistTask(ctx context.Context, runID, serverID string, status mcp.TaskStatus) (string, error) {
	if serverID == "" {
		// A NULL server_id means "internal", and an internal row that is
		// actually waiting on a remote server would be silently skipped by the
		// poller — a wait that can never be answered.
		return "", fmt.Errorf("audience entry has no mcp_servers row; a plugin-routed task cannot be polled without one")
	}

	id := model.NewULID()
	now := timeNow().UTC()
	stamp := now.Format(time.RFC3339Nano)
	params := db.CreateMCPTaskParams{
		ID:        id,
		RunID:     runID,
		ServerID:  &serverID,
		TaskID:    status.TaskID,
		Kind:      inapptask.KindChannelRequest,
		CreatedAt: stamp,
		UpdatedAt: stamp,
	}
	if status.PollInterval > 0 {
		ms := status.PollInterval.Milliseconds()
		params.PollIntervalMs = &ms
	}
	if status.TTL > 0 {
		// The server declares TTL as time REMAINING; the column stores the
		// absolute instant, which is what the poller's expiry check compares
		// against. Anchoring it here means a restart hours later reads the same
		// deadline rather than granting the task a fresh TTL.
		ttl := now.Add(status.TTL).Format(time.RFC3339Nano)
		params.ServerTtl = &ttl
	}
	if _, err := r.tasks.CreateMCPTask(ctx, params); err != nil {
		return "", fmt.Errorf("persist channel request task: %w", err)
	}
	return id, nil
}

// Complete settles a routed ask, whichever route it took.
//
// This is the exactly-one point (ADR-044). Arbitration is the row's CAS
// (ADR-038): the first writer to move the task out of a non-terminal status
// wins, and every later one gets ErrAlreadyResolved rather than silently
// overwriting a decision a human already made. Nothing here knows or cares
// which route produced the row — a plugin-routed completion arriving from the
// poller and an operator clicking in the UI contend on the same UPDATE.
func (r *Router) Complete(ctx context.Context, routed Routed, resolution inapptask.Resolution) error {
	if routed.RowID == "" {
		return inapptask.ErrUnknownTask
	}
	return r.completer.Complete(ctx, routed.RowID, resolution)
}

// validateAsk rejects an ask that could never be answered, before any channel
// is troubled with it. The rules match what both legs enforce for themselves —
// stated once here so a caller learns of the problem at the routing boundary
// rather than from whichever entry happened to be first.
func validateAsk(ask Ask) error {
	if ask.RunID == "" {
		return fmt.Errorf("hitl: run ID is required")
	}
	if ask.Message == "" {
		return fmt.Errorf("hitl: message is empty; there is nothing to ask")
	}
	if len(ask.Options) == 0 && len(ask.RequestedSchema) == 0 {
		return fmt.Errorf("hitl: ask carries neither options nor a requestedSchema; it is a notification, not a question")
	}
	if !ask.Kind.Valid() {
		// An unknown kind cannot be gated: MayResolve would refuse it
		// everywhere and the request would fall through the whole audience for
		// a reason nobody could act on. Refuse it where it can be named.
		return fmt.Errorf("hitl: elicitation kind %q is not %q or %q",
			ask.Kind, model.ElicitationKindPermission, model.ElicitationKindInformation)
	}
	return nil
}

// TargetFromConfig reads an audience entry's delivery target out of its config
// blob. The two keys are the channel-neutral vocabulary from spec §4.2; the
// address stays opaque, because what a channel calls an address is the
// channel's business and a host that parsed it would be a host with a
// per-channel special case.
//
// A blob that does not name both is not an error to shout about — it is an
// entry that cannot be routed to, which the router records as a skip.
func TargetFromConfig(configJSON string) (mcp.ChannelTarget, bool) {
	if strings.TrimSpace(configJSON) == "" {
		return mcp.ChannelTarget{}, false
	}
	var wire struct {
		Delivery string `json:"delivery"`
		Address  string `json:"address"`
	}
	if err := json.Unmarshal([]byte(configJSON), &wire); err != nil {
		return mcp.ChannelTarget{}, false
	}
	target := mcp.ChannelTarget{
		Delivery: mcp.ChannelDelivery(wire.Delivery),
		Address:  wire.Address,
	}
	if !target.Delivery.Valid() || target.Address == "" {
		return mcp.ChannelTarget{}, false
	}
	return target, true
}

// majorVersionSupported reports whether a declared contract version is one this
// client can read. Minor and patch are additive by policy (extension doc §3), so
// only the major matters; an absent or unparseable version is refused, because
// routing a human decision through a contract whose shape cannot be established
// is the guess that cannot be undone.
func majorVersionSupported(declared string) bool {
	want, ok := majorOf(mcp.ExtensionChannelVersion)
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

func toChannelOptions(options []inapptask.Option) []mcp.ChannelOption {
	if len(options) == 0 {
		return nil
	}
	out := make([]mcp.ChannelOption, len(options))
	for i, o := range options {
		out[i] = mcp.ChannelOption{ID: o.ID, Label: o.Label}
	}
	return out
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
