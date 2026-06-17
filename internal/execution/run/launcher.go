package run

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/infra/event"
	"github.com/felag-engineering/gleipnir/internal/infra/logctx"
	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/policy"
	"github.com/felag-engineering/gleipnir/internal/settings"
)

// AgentFactory constructs a BoundAgent from a fully-populated Config.
// The factory owns all decisions about how to supply the LLM client or any
// test doubles — callers have no knowledge of either.
type AgentFactory func(cfg agent.Config) (*agent.BoundAgent, error)

// NewAgentFactory returns an AgentFactory that resolves the correct LLM client
// from registry and constructs a BoundAgent for the run. If the policy's
// provider is not registered, the factory returns a descriptive error so the
// run record can be marked failed with a clear message.
func NewAgentFactory(registry *llm.ProviderRegistry) AgentFactory {
	return func(cfg agent.Config) (*agent.BoundAgent, error) {
		client, err := registry.Get(cfg.Policy.Agent.ModelConfig.Provider)
		if err != nil {
			return nil, fmt.Errorf("provider lookup: %w", err)
		}
		cfg.LLMClient = client
		return agent.New(cfg)
	}
}

// Sentinel errors returned by CheckConcurrency so callers can map them to HTTP
// status codes or log appropriately without inspecting error message strings.
var (
	ErrConcurrencySkipActive   = errors.New("run already active for this policy (concurrency: skip)")
	ErrConcurrencyQueueActive  = errors.New("run active, trigger should be queued")
	ErrConcurrencyQueueFull    = errors.New("trigger queue is full")
	ErrConcurrencyUnrecognised = errors.New("unrecognised concurrency policy")
)

// concurrencyCheckError wraps a non-sentinel error returned by CheckConcurrency
// (a real DB failure listing active runs). It exists so the HTTP adapter can
// map it to the distinct "failed to check active runs" 500 and background
// adapters can log "concurrency check failed", preserving the three separate
// failure messages the inline copies had.
type concurrencyCheckError struct{ err error }

func (e *concurrencyCheckError) Error() string { return e.err.Error() }
func (e *concurrencyCheckError) Unwrap() error { return e.err }

// enqueueError wraps a non-sentinel error from Enqueue (a real DB failure
// counting/inserting queued triggers), distinct from ErrConcurrencyQueueFull.
// Lets the HTTP adapter keep the "failed to enqueue trigger" 500 message.
type enqueueError struct{ err error }

func (e *enqueueError) Error() string { return e.err.Error() }
func (e *enqueueError) Unwrap() error { return e.err }

// IsConcurrencyCheckError reports whether err is a wrapped CheckConcurrency DB
// error. Use this predicate instead of naming the unexported type directly.
func IsConcurrencyCheckError(err error) bool {
	var e *concurrencyCheckError
	return errors.As(err, &e)
}

// IsEnqueueError reports whether err is a wrapped non-queue-full Enqueue DB
// error. Use this predicate instead of naming the unexported type directly.
func IsEnqueueError(err error) bool {
	var e *enqueueError
	return errors.As(err, &e)
}

// LaunchOutcome classifies the branch taken by LaunchWithConcurrency.
type LaunchOutcome int

const (
	// OutcomeLaunched means the run was launched immediately.
	OutcomeLaunched LaunchOutcome = iota
	// OutcomeQueued means an active run existed and the trigger was enqueued for
	// later execution (concurrency: queue).
	OutcomeQueued
	// OutcomeSkipped means an active run existed and the trigger was dropped
	// (concurrency: skip).
	OutcomeSkipped
)

// replaceCancelTimeout is how long CheckConcurrency waits for a cancelled run's
// goroutine to exit before proceeding with the new run. Long enough for the agent
// loop to observe context cancellation; short enough to not stall trigger handlers.
const replaceCancelTimeout = 5 * time.Second

// LaunchParams carries all the inputs needed to create and start a run.
type LaunchParams struct {
	PolicyID       string
	TriggerType    model.TriggerType
	TriggerPayload string // valid JSON string
	ParsedPolicy   *model.ParsedPolicy
}

// LaunchResult carries the output of a Launch or LaunchWithConcurrency call.
// RunID is populated on success and on any failure path that occurred *after*
// the run row was created — in those cases the row has been transitioned to
// status=failed with the underlying error stored on it, so callers can
// deep-link to the run detail page. RunID is empty for OutcomeQueued,
// OutcomeSkipped, and for errors that occur before any run row is created
// (CheckConcurrency DB error, queue-full, unrecognised, enqueue error).
// Outcome identifies which branch fired; it is only meaningful when error is nil.
type LaunchResult struct {
	RunID   string
	Outcome LaunchOutcome
}

// RunLauncher encapsulates the shared logic for creating a run record,
// resolving tools, constructing the agent, and launching the goroutine.
// All three trigger handlers (webhook, manual, scheduled) delegate to it.
type RunLauncher struct {
	store                  *db.Store
	resolver               ToolResolver
	manager                *RunManager
	newAgent               AgentFactory
	publisher              event.Publisher
	defaultFeedbackTimeout time.Duration
	modelResolver          *settings.Service
	pluginRegistrar        agent.PluginGenerationLookup
	pluginDispatcher       agent.PluginToolDispatcher
	approvalDispatcher     agent.ApprovalChannelDispatcher
	feedbackDispatcher     agent.FeedbackChannelDispatcher
}

// RunLauncherConfig holds all dependencies for a RunLauncher. Field order
// mirrors the former positional parameters so a grep-diff is auditable.
type RunLauncherConfig struct {
	Store                  *db.Store
	Resolver               ToolResolver
	Manager                *RunManager
	AgentFactory           AgentFactory
	Publisher              event.Publisher // nil = no real-time events
	DefaultFeedbackTimeout time.Duration
	ModelResolver          *settings.Service            // nil = use launch-time snapshot only
	PluginRegistrar        agent.PluginGenerationLookup // nil when plugins are disabled
	PluginDispatcher       agent.PluginToolDispatcher   // nil when plugins are disabled
	// ApprovalDispatcher routes approvals through a plugin channel when the
	// policy has an audience configured.  Nil when plugins are disabled.
	ApprovalDispatcher agent.ApprovalChannelDispatcher
	// FeedbackDispatcher routes feedback requests through a plugin channel when
	// the policy has an audience configured.  Nil when plugins are disabled.
	FeedbackDispatcher agent.FeedbackChannelDispatcher
}

// NewRunLauncher returns a RunLauncher ready to use.
// publisher may be nil, in which case no real-time events are emitted.
// defaultFeedbackTimeout is used when a policy does not specify its own timeout.
// modelResolver is used by the drain path to re-parse policies with current
// system settings; it may be nil, in which case the drain path uses the
// snapshot captured at launch time.
func NewRunLauncher(cfg RunLauncherConfig) *RunLauncher {
	return &RunLauncher{
		store:                  cfg.Store,
		resolver:               cfg.Resolver,
		manager:                cfg.Manager,
		newAgent:               cfg.AgentFactory,
		publisher:              cfg.Publisher,
		defaultFeedbackTimeout: cfg.DefaultFeedbackTimeout,
		modelResolver:          cfg.ModelResolver,
		pluginRegistrar:        cfg.PluginRegistrar,
		pluginDispatcher:       cfg.PluginDispatcher,
		approvalDispatcher:     cfg.ApprovalDispatcher,
		feedbackDispatcher:     cfg.FeedbackDispatcher,
	}
}

// LaunchWithConcurrency is the single entry point for all trigger types. It
// enforces the concurrency policy declared in params.ParsedPolicy, then either
// skips, enqueues, or launches the run. It owns all choreography so trigger
// handlers reduce to thin adapters that only handle their transport-specific
// reaction (HTTP status vs. structured log).
//
// Skip and queue are returned as (LaunchResult{Outcome: …}, nil) — they are
// non-error outcomes. A non-nil error is returned only for genuine failures:
//   - ErrConcurrencyQueueFull (verbatim) — queue is at capacity
//   - ErrConcurrencyUnrecognised (verbatim) — unknown concurrency policy
//   - IsConcurrencyCheckError(err) == true — raw DB error from CheckConcurrency
//   - IsEnqueueError(err) == true — raw DB error from Enqueue
//   - any other error — from Launch (may carry a RunID in the result)
func (l *RunLauncher) LaunchWithConcurrency(ctx context.Context, params LaunchParams) (LaunchResult, error) {
	concurrency := params.ParsedPolicy.Agent.Concurrency
	queueDepth := params.ParsedPolicy.Agent.QueueDepth

	err := l.CheckConcurrency(ctx, params.PolicyID, concurrency)
	switch {
	case err == nil:
		// fall through to Launch.
	case errors.Is(err, ErrConcurrencySkipActive):
		return LaunchResult{Outcome: OutcomeSkipped}, nil
	case errors.Is(err, ErrConcurrencyQueueActive):
		if enqErr := l.Enqueue(ctx, params, queueDepth); enqErr != nil {
			if errors.Is(enqErr, ErrConcurrencyQueueFull) {
				return LaunchResult{}, ErrConcurrencyQueueFull // verbatim so errors.Is works
			}
			return LaunchResult{}, &enqueueError{err: enqErr}
		}
		return LaunchResult{Outcome: OutcomeQueued}, nil
	case errors.Is(err, ErrConcurrencyUnrecognised):
		return LaunchResult{}, ErrConcurrencyUnrecognised // verbatim so errors.Is works
	default:
		// Real DB error from CheckConcurrency. Wrap so adapters can distinguish
		// "failed to check active runs" from a launch failure.
		return LaunchResult{}, &concurrencyCheckError{err: err}
	}

	res, err := l.Launch(ctx, params)
	// On Launch error, res.RunID may be set (failure after the row was created).
	// Propagate it so HTTP adapters can deep-link to the failed run row via
	// WriteLaunchError — matches the pre-refactor behavior.
	return LaunchResult{RunID: res.RunID, Outcome: OutcomeLaunched}, err
}

// drainResolveDefaults fetches the system default model for the drain path.
// When no resolver is configured, or the resolver returns an error, it returns
// ("", "") — the caller falls back to the launch-time snapshot. Drain is
// best-effort, so we never block queued runs on a resolver failure.
func (l *RunLauncher) drainResolveDefaults(ctx context.Context) (string, string) {
	if l.modelResolver == nil {
		return "", ""
	}
	provider, modelName, err := l.modelResolver.GetSystemDefault(ctx)
	if err != nil {
		return "", ""
	}
	return provider, modelName
}

// CheckConcurrency enforces the given concurrency policy for the policy
// identified by policyID. Returns nil if the run should proceed, or one of the
// sentinel errors (ErrConcurrencySkipActive, ErrConcurrencyQueueActive) if the
// caller must take action (skip or enqueue). For replace mode, any active runs
// are cancelled and their goroutines are awaited before returning nil.
func (l *RunLauncher) CheckConcurrency(ctx context.Context, policyID string, concurrency model.ConcurrencyPolicy) error {
	switch concurrency {
	case model.ConcurrencySkip:
		active, err := l.store.ListActiveRunsByPolicy(ctx, policyID)
		if err != nil {
			return fmt.Errorf("list active runs for policy %q: %w", policyID, err)
		}
		if len(active) > 0 {
			return ErrConcurrencySkipActive
		}
		return nil
	case model.ConcurrencyParallel:
		return nil
	case model.ConcurrencyQueue:
		active, err := l.store.ListActiveRunsByPolicy(ctx, policyID)
		if err != nil {
			return fmt.Errorf("list active runs for policy %q: %w", policyID, err)
		}
		if len(active) > 0 {
			return ErrConcurrencyQueueActive
		}
		return nil
	case model.ConcurrencyReplace:
		active, err := l.store.ListActiveRunsByPolicy(ctx, policyID)
		if err != nil {
			return fmt.Errorf("list active runs for policy %q: %w", policyID, err)
		}
		for _, run := range active {
			l.manager.Cancel(run.ID)
			// Wait for the cancelled run's goroutine to exit before starting the new run.
			// This keeps DB state consistent: the terminal status write from the outgoing
			// run happens before CreateRun for the incoming run. If the goroutine takes
			// longer than the deadline, we proceed anyway — don't block indefinitely
			// (see issue #521).
			if !l.manager.WaitForDeregistration(run.ID, replaceCancelTimeout) {
				slog.Warn("replace: cancelled run did not exit within deadline, proceeding",
					"policy_id", policyID, "cancelled_run_id", run.ID)
			}
		}
		return nil
	default:
		return ErrConcurrencyUnrecognised
	}
}

// Launch creates a run record, resolves tools, constructs the agent, and
// launches it in a background goroutine. On any setup error after the run row
// is created, the run is marked failed before returning.
// Returns LaunchResult with the new run ID on success. On failure paths that
// occurred after the run row was created, LaunchResult.RunID is also set so
// the caller can surface a deep-link to the failed run row (which already has
// the underlying error recorded on it).
func (l *RunLauncher) Launch(ctx context.Context, params LaunchParams) (LaunchResult, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	run, err := l.store.CreateRun(ctx, db.CreateRunParams{
		ID:             model.NewULID(),
		PolicyID:       params.PolicyID,
		Model:          params.ParsedPolicy.Agent.ModelConfig.Name,
		TriggerType:    string(params.TriggerType),
		TriggerPayload: params.TriggerPayload,
		StartedAt:      now,
		CreatedAt:      now,
	})
	if err != nil {
		return LaunchResult{}, fmt.Errorf("create run for policy %q: %w", params.PolicyID, err)
	}

	sm := agent.NewRunStateMachine(
		run.ID,
		model.RunStatus(run.Status),
		l.store.DB(),
		l.store.Queries(),
		agent.WithStateMachinePublisher(l.publisher),
		agent.WithInitialVersion(run.Version),
	)

	// Resolve all capability grants in one call. Classification (MCP vs plugin),
	// per-source resolution, and error handling are encapsulated in the resolver.
	// context.Background() is used for the fail-run transition because the HTTP
	// request context that produced ctx may already be cancelled when this error
	// surfaces, but the DB write must complete so the run does not linger in
	// 'pending' indefinitely.
	toolSet, resolveErr := l.resolver.ResolveCapabilities(ctx, params.ParsedPolicy.Capabilities.Tools)
	if resolveErr != nil {
		if tErr := sm.Transition(context.Background(), model.RunStatusFailed, resolveErr.Error()); tErr != nil {
			if errors.Is(tErr, agent.ErrTransitionConflict) {
				slog.Info("transition lost to concurrent writer on tool resolution error", "run_id", run.ID)
			} else {
				slog.Error("transition to failed on tool resolution error", "run_id", run.ID, "err", tErr)
			}
		}
		return LaunchResult{RunID: run.ID}, resolveErr
	}

	audit := agent.NewAuditWriter(l.store.Queries(), agent.WithPublisher(l.publisher))

	// Resolve the audience DB row ID when a plugin channel dispatcher is available
	// and the policy names an audience.  A misspelled or missing audience name logs
	// a warning and falls back to in-app rather than failing the run — the warning
	// makes the misconfiguration visible without hard-blocking the operator.
	var audienceID string
	if params.ParsedPolicy.Audience != "" && l.approvalDispatcher != nil {
		aud, audErr := l.store.Queries().GetPluginAudienceByName(ctx, params.ParsedPolicy.Audience)
		if audErr != nil {
			slog.Warn("audience lookup failed, falling back to in-app approval",
				"audience_name", params.ParsedPolicy.Audience,
				"policy_id", params.PolicyID,
				"err", audErr,
			)
		} else {
			audienceID = aud.ID
		}
	}

	// Cap 1 so SendApproval (non-blocking select) can deliver a decision that
	// arrives in the narrow window between the agent unparking and reading the
	// channel.
	approvalCh := make(chan bool, 1)
	ba, err := l.newAgent(agent.Config{
		Tools:                  toolSet.MCPTools,
		Policy:                 params.ParsedPolicy,
		PolicyID:               params.PolicyID,
		Audit:                  audit,
		StateMachine:           sm,
		ApprovalCh:             approvalCh,
		DefaultFeedbackTimeout: l.defaultFeedbackTimeout,
		PluginTools:            toolSet.PluginTools,
		PluginRegistrar:        l.pluginRegistrar,
		PluginDispatcher:       l.pluginDispatcher,
		ApprovalDispatcher:     l.approvalDispatcher,
		FeedbackDispatcher:     l.feedbackDispatcher,
		AudienceID:             audienceID,
	})
	if err != nil {
		// context.Background(): the HTTP request context that produced ctx may
		// already be cancelled, but the DB write must complete so the run does
		// not linger in 'pending' indefinitely.
		if tErr := sm.Transition(context.Background(), model.RunStatusFailed, err.Error()); tErr != nil {
			if errors.Is(tErr, agent.ErrTransitionConflict) {
				slog.Info("transition lost to concurrent writer on agent construction error", "run_id", run.ID)
			} else {
				slog.Error("transition to failed on agent construction error", "run_id", run.ID, "err", tErr)
			}
		}
		if closeErr := audit.Close(); closeErr != nil {
			slog.Error("audit writer drain error on failed launch", "run_id", run.ID, "err", closeErr)
		}
		// Return RunID alongside the error so callers can link to the failed
		// run row. The row already has the underlying error stored on it.
		return LaunchResult{RunID: run.ID}, err
	}

	// context.Background() is used intentionally so the agent goroutine outlives
	// the HTTP request that triggered it. RunManager's WaitGroup tracks it for
	// graceful shutdown; cancellation is performed via the registered cancel func.
	runCtx, cancel := context.WithCancel(context.Background())
	// Enrich the run context with correlation IDs so all downstream log calls
	// automatically include run_id and policy_id in structured output.
	runCtx = logctx.WithRunCorrelation(runCtx, run.ID, params.PolicyID)
	// RegisterWithFeedbackResolver performs a single atomic lock acquisition so
	// there is zero window between run registration and resolver attachment.
	l.manager.RegisterWithFeedbackResolver(run.ID, cancel, approvalCh, ba.FeedbackResolver())

	payload := params.TriggerPayload
	go func() {
		defer cancel()
		defer l.manager.Deregister(run.ID)
		l.runAndDrain(runCtx, run.ID, params.TriggerType, params.PolicyID, params.ParsedPolicy, payload, ba)
	}()

	return LaunchResult{RunID: run.ID}, nil
}

// runAndDrain executes the agent run and, if the policy uses queue concurrency,
// drains the next trigger from the queue. It is the body of the goroutine
// launched by Launch — extracted so it can be tested independently.
//
// ctx should be the run-scoped context (already enriched with correlation IDs).
// Use context.Background() for the drain step because ctx may be cancelled by
// the time the run completes.
func (l *RunLauncher) runAndDrain(ctx context.Context, runID string, triggerType model.TriggerType, policyID string, parsedPolicy *model.ParsedPolicy, payload string, ba *agent.BoundAgent) {
	if err := ba.Run(ctx, runID, payload); err != nil {
		logctx.Logger(ctx).ErrorContext(ctx, "run failed", "trigger_type", string(triggerType), "err", err)
	}
	// Drain the queue if this policy uses queue concurrency.
	// ba.Run has completed so the run's DB status is terminal — DrainQueue's
	// ListActiveRunsByPolicy (called inside Launch) will not see this run.
	// Use context.Background() because ctx may be cancelled.
	// Re-fetch the policy so DrainQueue uses current settings (queue_depth,
	// concurrency) rather than a snapshot captured at launch time.
	if parsedPolicy.Agent.Concurrency == model.ConcurrencyQueue {
		drainCtx := context.Background()
		currentPolicy := parsedPolicy
		if dbPol, dbErr := l.store.GetPolicy(drainCtx, policyID); dbErr == nil {
			provider, modelName := l.drainResolveDefaults(drainCtx)
			if provider == "" || modelName == "" {
				slog.Warn("drain: system default model unavailable, using launch-time snapshot",
					"policy_id", policyID)
			} else if p, parseErr := policy.Parse(dbPol.Yaml, provider, modelName); parseErr == nil {
				currentPolicy = p
			} else {
				slog.Warn("drain: failed to re-parse policy, using launch-time snapshot",
					"policy_id", policyID, "err", parseErr)
			}
		}
		l.DrainQueue(drainCtx, policyID, currentPolicy)
	}
}

// Enqueue checks queue depth and enqueues the trigger payload.
// Returns ErrConcurrencyQueueFull if the queue is at capacity.
//
// The count-then-insert is not wrapped in an explicit transaction.
// Safety relies on db.SetMaxOpenConns(1) (store.go) which serializes all
// DB access through a single connection. If that constraint is ever
// relaxed, this must be wrapped in a BEGIN/COMMIT to prevent TOCTOU races
// that could allow queue depth to be exceeded by one.
func (l *RunLauncher) Enqueue(ctx context.Context, params LaunchParams, queueDepth int) error {
	count, err := l.store.CountQueuedTriggers(ctx, params.PolicyID)
	if err != nil {
		return fmt.Errorf("count queued triggers: %w", err)
	}
	if count >= int64(queueDepth) {
		return ErrConcurrencyQueueFull
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = l.store.EnqueueTrigger(ctx, db.EnqueueTriggerParams{
		ID:             model.NewULID(),
		PolicyID:       params.PolicyID,
		TriggerType:    string(params.TriggerType),
		TriggerPayload: params.TriggerPayload,
		CreatedAt:      now,
	})
	if err != nil {
		return fmt.Errorf("enqueue trigger: %w", err)
	}
	return nil
}

// DrainQueue dequeues the next trigger for the policy and launches it.
// DequeueTrigger is a DELETE…RETURNING — the row is removed immediately. If
// Launch fails, the entry is re-inserted at the front of the queue via
// RequeueTriggerAtFront so FIFO ordering is preserved.
// Called after a run reaches a terminal state; not a periodic loop.
// Errors are logged because the caller is a fire-and-forget goroutine.
// This is a no-op when the queue is empty.
func (l *RunLauncher) DrainQueue(ctx context.Context, policyID string, parsedPolicy *model.ParsedPolicy) {
	entry, err := l.store.DequeueTrigger(ctx, policyID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("drain queue: failed to dequeue trigger",
				"policy_id", policyID, "err", err)
		}
		return
	}

	_, launchErr := l.Launch(ctx, LaunchParams{
		PolicyID:       policyID,
		TriggerType:    model.TriggerType(entry.TriggerType),
		TriggerPayload: entry.TriggerPayload,
		ParsedPolicy:   parsedPolicy,
	})
	if launchErr != nil {
		// Launch failed. Re-enqueue at front to preserve FIFO ordering.
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, reErr := l.store.RequeueTriggerAtFront(ctx, db.RequeueTriggerAtFrontParams{
			ID:             entry.ID,
			PolicyID:       policyID,
			TriggerType:    entry.TriggerType,
			TriggerPayload: entry.TriggerPayload,
			CreatedAt:      now,
		}); reErr != nil {
			slog.Error("drain queue: failed to re-enqueue after launch failure",
				"policy_id", policyID, "queue_entry_id", entry.ID,
				"launch_err", launchErr, "re_enqueue_err", reErr)
		} else {
			slog.Warn("drain queue: launch failed, entry re-enqueued at front for retry",
				"policy_id", policyID, "queue_entry_id", entry.ID, "err", launchErr)
		}
	}
}
