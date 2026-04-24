package run

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/infra/event"
	"github.com/rapp992/gleipnir/internal/execution/agent"
	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/infra/logctx"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
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

// LaunchResult carries the output of a successful Launch call.
type LaunchResult struct {
	RunID string
}

// defaultModelResolver fetches the system-wide default LLM provider and model
// name. *admin.Handler satisfies this interface. Used by the drain path to
// re-parse policies with current settings rather than a stale snapshot.
type defaultModelResolver interface {
	GetSystemDefault(ctx context.Context) (provider string, modelName string, err error)
}

// RunLauncher encapsulates the shared logic for creating a run record,
// resolving tools, constructing the agent, and launching the goroutine.
// All three trigger handlers (webhook, manual, scheduled) delegate to it.
type RunLauncher struct {
	store                  *db.Store
	registry               registryResolver
	manager                *RunManager
	newAgent               AgentFactory
	publisher              event.Publisher
	defaultFeedbackTimeout time.Duration
	modelResolver          defaultModelResolver
}

// registryResolver is the subset of mcp.Registry used by RunLauncher, defined
// as an interface so tests can supply a stub without importing internal/mcp directly.
type registryResolver interface {
	ResolveForPolicy(ctx context.Context, p *model.ParsedPolicy) ([]mcp.ResolvedTool, error)
}

// NewRunLauncher returns a RunLauncher ready to use.
// publisher may be nil, in which case no real-time events are emitted.
// defaultFeedbackTimeout is used when a policy does not specify its own timeout.
// modelResolver is used by the drain path to re-parse policies with current
// system settings; it may be nil, in which case the drain path uses the
// snapshot captured at launch time.
func NewRunLauncher(store *db.Store, registry registryResolver, manager *RunManager, factory AgentFactory, publisher event.Publisher, defaultFeedbackTimeout time.Duration, modelResolver defaultModelResolver) *RunLauncher {
	return &RunLauncher{
		store:                  store,
		registry:               registry,
		manager:                manager,
		newAgent:               factory,
		publisher:              publisher,
		defaultFeedbackTimeout: defaultFeedbackTimeout,
		modelResolver:          modelResolver,
	}
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
// Returns LaunchResult with the new run ID on success.
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

	resolvedTools, err := l.registry.ResolveForPolicy(ctx, params.ParsedPolicy)
	if err != nil {
		// context.Background(): the HTTP request context that produced ctx may
		// already be cancelled, but the DB write must complete so the run does
		// not linger in 'pending' indefinitely.
		if tErr := sm.Transition(context.Background(), model.RunStatusFailed, err.Error()); tErr != nil {
			if errors.Is(tErr, agent.ErrTransitionConflict) {
				slog.Info("transition lost to concurrent writer on tool resolution error", "run_id", run.ID)
			} else {
				slog.Error("transition to failed on tool resolution error", "run_id", run.ID, "err", tErr)
			}
		}
		return LaunchResult{}, err
	}

	audit := agent.NewAuditWriter(l.store.Queries(), agent.WithPublisher(l.publisher))

	// Cap 1 so SendApproval/SendFeedback (non-blocking select) can deliver a
	// decision that arrives in the narrow window between the agent unparking and
	// reading the channel. Protocol is single-producer/single-consumer per gate,
	// so cap 1 is sufficient and extra sends are still correctly dropped.
	approvalCh := make(chan bool, 1)
	feedbackCh := make(chan string, 1)
	ba, err := l.newAgent(agent.Config{
		Tools:                  resolvedTools,
		Policy:                 params.ParsedPolicy,
		Audit:                  audit,
		StateMachine:           sm,
		ApprovalCh:             approvalCh,
		FeedbackCh:             feedbackCh,
		DefaultFeedbackTimeout: l.defaultFeedbackTimeout,
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
		return LaunchResult{}, err
	}

	// context.Background() is used intentionally so the agent goroutine outlives
	// the HTTP request that triggered it. RunManager's WaitGroup tracks it for
	// graceful shutdown; cancellation is performed via the registered cancel func.
	runCtx, cancel := context.WithCancel(context.Background())
	// Enrich the run context with correlation IDs so all downstream log calls
	// automatically include run_id and policy_id in structured output.
	runCtx = logctx.WithRunCorrelation(runCtx, run.ID, params.PolicyID)
	l.manager.Register(run.ID, cancel, approvalCh, feedbackCh)

	payload := params.TriggerPayload
	go func() {
		defer cancel()
		defer l.manager.Deregister(run.ID)
		if err := ba.Run(runCtx, run.ID, payload); err != nil {
			logctx.Logger(runCtx).ErrorContext(runCtx, "run failed", "trigger_type", string(params.TriggerType), "err", err)
		}
		// Drain the queue if this policy uses queue concurrency.
		// ba.Run has completed so the run's DB status is terminal — DrainQueue's
		// ListActiveRunsByPolicy (called inside Launch) will not see this run.
		// Use context.Background() because runCtx may be cancelled.
		// Re-fetch the policy so DrainQueue uses current settings (queue_depth,
		// concurrency) rather than a snapshot captured at launch time.
		if params.ParsedPolicy.Agent.Concurrency == model.ConcurrencyQueue {
			drainCtx := context.Background()
			currentPolicy := params.ParsedPolicy
			if dbPol, dbErr := l.store.GetPolicy(drainCtx, params.PolicyID); dbErr == nil {
				provider, modelName := l.drainResolveDefaults(drainCtx)
				if provider == "" || modelName == "" {
					slog.Warn("drain: system default model unavailable, using launch-time snapshot",
						"policy_id", params.PolicyID)
				} else if p, parseErr := policy.Parse(dbPol.Yaml, provider, modelName); parseErr == nil {
					currentPolicy = p
				} else {
					slog.Warn("drain: failed to re-parse policy, using launch-time snapshot",
						"policy_id", params.PolicyID, "err", parseErr)
				}
			}
			l.DrainQueue(drainCtx, params.PolicyID, currentPolicy)
		}
	}()

	return LaunchResult{RunID: run.ID}, nil
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
