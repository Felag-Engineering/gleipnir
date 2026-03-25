package trigger

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/rapp992/gleipnir/internal/agent"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/mcp"
	"github.com/rapp992/gleipnir/internal/model"
)

// AgentFactory constructs a BoundAgent from a fully-populated Config.
// The factory owns all decisions about how to supply the Claude client or any
// test doubles — callers have no knowledge of either.
type AgentFactory func(cfg agent.Config) (*agent.BoundAgent, error)

// NewAgentFactory returns an AgentFactory that injects claude into cfg before
// calling agent.New. Use this in production.
func NewAgentFactory(claude *anthropic.Client) AgentFactory {
	return func(cfg agent.Config) (*agent.BoundAgent, error) {
		cfg.Claude = claude
		return agent.New(cfg)
	}
}

// Sentinel errors returned by CheckConcurrency so callers can map them to HTTP
// status codes or log appropriately without inspecting error message strings.
var (
	ErrConcurrencySkipActive     = errors.New("run already active for this policy (concurrency: skip)")
	ErrConcurrencyNotImplemented = errors.New("concurrency policy not implemented")
	ErrConcurrencyUnrecognised   = errors.New("unrecognised concurrency policy")
)

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

// RunLauncher encapsulates the shared logic for creating a run record,
// resolving tools, constructing the agent, and launching the goroutine.
// All three trigger handlers (webhook, manual, scheduled) delegate to it.
type RunLauncher struct {
	store     *db.Store
	registry  registryResolver
	manager   *RunManager
	newAgent  AgentFactory
	publisher agent.Publisher
}

// registryResolver is the subset of mcp.Registry used by RunLauncher, defined
// as an interface so tests can supply a stub without importing internal/mcp directly.
type registryResolver interface {
	ResolveForPolicy(ctx context.Context, p *model.ParsedPolicy) ([]mcp.ResolvedTool, error)
}

// NewRunLauncher returns a RunLauncher ready to use.
// publisher may be nil, in which case no real-time events are emitted.
func NewRunLauncher(store *db.Store, registry registryResolver, manager *RunManager, factory AgentFactory, publisher agent.Publisher) *RunLauncher {
	return &RunLauncher{
		store:     store,
		registry:  registry,
		manager:   manager,
		newAgent:  factory,
		publisher: publisher,
	}
}

// CheckConcurrency enforces the given concurrency policy for the policy
// identified by policyID. Returns nil if the run should proceed, or one of the
// sentinel errors if it should be rejected.
func (l *RunLauncher) CheckConcurrency(ctx context.Context, policyID string, concurrency model.ConcurrencyPolicy) error {
	switch concurrency {
	case model.ConcurrencySkip:
		active, err := l.store.ListActiveRunsByPolicy(ctx, policyID)
		if err != nil {
			return err
		}
		if len(active) > 0 {
			return ErrConcurrencySkipActive
		}
		return nil
	case model.ConcurrencyParallel:
		return nil
	case model.ConcurrencyQueue, model.ConcurrencyReplace:
		return ErrConcurrencyNotImplemented
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
		TriggerType:    string(params.TriggerType),
		TriggerPayload: params.TriggerPayload,
		StartedAt:      now,
		CreatedAt:      now,
	})
	if err != nil {
		return LaunchResult{}, err
	}

	resolvedTools, err := l.registry.ResolveForPolicy(ctx, params.ParsedPolicy)
	if err != nil {
		// Mark the run failed before returning — it was created but cannot proceed.
		markRunFailed(l.store, run.ID, err)
		return LaunchResult{}, err
	}

	audit := agent.NewAuditWriter(l.store.Queries(), agent.WithPublisher(l.publisher))
	sm := agent.NewRunStateMachine(run.ID, model.RunStatusPending, l.store.Queries(), agent.WithStateMachinePublisher(l.publisher))

	approvalCh := make(chan bool)
	ba, err := l.newAgent(agent.Config{
		Tools:        resolvedTools,
		Policy:       params.ParsedPolicy,
		Audit:        audit,
		StateMachine: sm,
		ApprovalCh:   approvalCh,
	})
	if err != nil {
		// Mark the run failed — it was created and tools resolved but agent
		// construction failed (e.g. schema narrowing error). Without this,
		// the run stays in 'pending' forever since ScanOrphanedRuns only
		// rescues 'running' and 'waiting_for_approval' states.
		markRunFailed(l.store, run.ID, err)
		if closeErr := audit.Close(); closeErr != nil {
			slog.Error("audit writer drain error on failed launch", "run_id", run.ID, "err", closeErr)
		}
		return LaunchResult{}, err
	}

	// context.Background() is used intentionally so the agent goroutine outlives
	// the HTTP request that triggered it. RunManager's WaitGroup tracks it for
	// graceful shutdown; cancellation is performed via the registered cancel func.
	runCtx, cancel := context.WithCancel(context.Background())
	l.manager.Register(run.ID, cancel, approvalCh)

	payload := params.TriggerPayload
	go func() {
		defer cancel()
		defer l.manager.Deregister(run.ID)
		if err := ba.Run(runCtx, run.ID, payload); err != nil {
			slog.Error("run failed", "run_id", run.ID, "trigger_type", string(params.TriggerType), "err", err)
		}
	}()

	return LaunchResult{RunID: run.ID}, nil
}

// markRunFailed transitions a run that was created but cannot proceed to the
// failed state. Called on error paths after CreateRun succeeds so the run
// does not linger in 'pending' indefinitely.
func markRunFailed(store *db.Store, runID string, origErr error) {
	failedAt := time.Now().UTC().Format(time.RFC3339Nano)
	errMsg := origErr.Error()
	// context.Background() strategy: called on error paths after the HTTP request
	// context may have been cancelled. The DB write must complete so the run does
	// not linger in 'pending' indefinitely.
	if err := store.UpdateRunError(context.Background(), db.UpdateRunErrorParams{
		Status:      string(model.RunStatusFailed),
		Error:       &errMsg,
		CompletedAt: &failedAt,
		ID:          runID,
	}); err != nil {
		slog.Error("mark run failed: persist status failed",
			"run_id", runID, "cause", origErr, "err", err)
	}
}
