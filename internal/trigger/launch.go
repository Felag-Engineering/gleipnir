package trigger

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/policy"
	"github.com/felag-engineering/gleipnir/internal/settings"
)

// fetchAndParsePolicy loads a policy by ID, resolves the system default model
// (used when the policy YAML omits the model block), and parses the YAML.
// If the resolved model is empty and the policy also omits the model block the
// run cannot proceed; the handler writes a 500 and returns nil.
// On any other failure it writes the appropriate HTTP error and returns nil.
func fetchAndParsePolicy(ctx context.Context, w http.ResponseWriter, store *db.Store, policyID string, resolver *settings.Service) *model.ParsedPolicy {
	dbPolicy, err := store.GetPolicy(ctx, policyID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, "policy not found", "")
			return nil
		}
		httputil.WriteError(w, http.StatusInternalServerError, "failed to load policy", "")
		return nil
	}

	if dbPolicy.PausedAt != nil {
		httputil.WriteError(w, http.StatusConflict, "policy is paused", "")
		return nil
	}

	// GetSystemDefault swallows sql.ErrNoRows and returns ("", "", nil) when no
	// default is configured, so any non-nil error here is a real DB failure.
	provider, modelName, err := resolver.GetSystemDefault(ctx)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to load system default model", "")
		return nil
	}
	// Empty provider means no system default is configured — pass ("", "") so
	// policy.Parse leaves ModelConfig blank; Validate will catch it if the
	// policy YAML also omits the model block.

	parsed, err := policy.Parse(dbPolicy.Yaml, provider, modelName)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to parse policy", "")
		return nil
	}

	if parsed.Agent.ModelConfig.Provider == "" || parsed.Agent.ModelConfig.Name == "" {
		httputil.WriteError(w, http.StatusInternalServerError,
			"no default model configured: set admin → models default or specify model in policy YAML", "")
		return nil
	}

	return parsed
}

// checkConcurrencyAndLaunch enforces the concurrency policy, enqueues the
// trigger if needed, or launches the run immediately. It writes the HTTP
// response in all cases. logPrefix identifies the trigger type in log messages
// (e.g. "webhook" or "manual trigger").
func checkConcurrencyAndLaunch(
	ctx context.Context,
	w http.ResponseWriter,
	launcher *run.RunLauncher,
	params run.LaunchParams,
	concurrency model.ConcurrencyPolicy,
	queueDepth int,
	logPrefix string,
) {
	if err := launcher.CheckConcurrency(ctx, params.PolicyID, concurrency); err != nil {
		switch {
		case errors.Is(err, run.ErrConcurrencySkipActive):
			httputil.WriteError(w, http.StatusConflict, "run already active for this policy (concurrency: skip)", "")
		case errors.Is(err, run.ErrConcurrencyQueueActive):
			if enqErr := launcher.Enqueue(ctx, params, queueDepth); enqErr != nil {
				if errors.Is(enqErr, run.ErrConcurrencyQueueFull) {
					httputil.WriteError(w, http.StatusTooManyRequests, "trigger queue is full", "")
				} else {
					slog.ErrorContext(ctx, logPrefix+": failed to enqueue trigger", "policy_id", params.PolicyID, "err", enqErr)
					httputil.WriteError(w, http.StatusInternalServerError, "failed to enqueue trigger", "")
				}
				return
			}
			httputil.WriteJSON(w, http.StatusAccepted, map[string]any{"queued": true})
		case errors.Is(err, run.ErrConcurrencyUnrecognised):
			httputil.WriteError(w, http.StatusInternalServerError, "unrecognised concurrency policy", "")
		default:
			slog.ErrorContext(ctx, logPrefix+": failed to check active runs", "policy_id", params.PolicyID, "err", err)
			httputil.WriteError(w, http.StatusInternalServerError, "failed to check active runs", "")
		}
		return
	}

	result, err := launcher.Launch(ctx, params)
	if err != nil {
		// Log with the underlying error and run_id (when populated) so
		// operators can correlate logs to the failed run row in history.
		slog.ErrorContext(ctx, logPrefix+": failed to launch run",
			"policy_id", params.PolicyID,
			"run_id", result.RunID,
			"err", err,
		)
		// Surface err.Error() as the response detail so the caller sees the
		// real reason (e.g. `tool "my-server.foo" not found in registry`)
		// instead of a generic 500. When result.RunID is non-empty, the run
		// row was created and marked failed — include it so the UI can deep
		// link to the run detail page where the recorded error is visible.
		httputil.WriteLaunchError(w, http.StatusInternalServerError,
			"failed to launch run", err.Error(), result.RunID)
		return
	}

	httputil.WriteJSON(w, http.StatusAccepted, map[string]string{"run_id": result.RunID})
}

// launchOutcome reports what launchOrQueueBackground did so background callers
// can take follow-up action (e.g. the scheduler auto-pausing a policy whose
// fire time was consumed by a skip, enqueue, or launch).
type launchOutcome int

const (
	// outcomeSkipped: an active run exists and concurrency is "skip" — the
	// trigger was dropped. The fire time is considered consumed.
	outcomeSkipped launchOutcome = iota
	// outcomeQueued: an active run exists and concurrency is "queue" — the
	// trigger was enqueued for later. The fire time is considered consumed.
	outcomeQueued
	// outcomeLaunched: the run was launched immediately.
	outcomeLaunched
	// outcomeError: concurrency check, enqueue, or launch failed; the run did
	// not start and was not queued. The error was already logged.
	outcomeError
)

// launchOrQueueBackground mirrors checkConcurrencyAndLaunch for background
// triggers (poll, cron, scheduled) that have no http.ResponseWriter: it enforces
// the concurrency policy, enqueues or launches a run, logs the result with the
// given logPrefix ("poller"/"cron"/"scheduled"), and returns a structured
// outcome so callers can react. logAttrs are appended to every log line emitted
// here so per-trigger context (e.g. "fired_at"/"fire_at") is preserved.
func launchOrQueueBackground(
	ctx context.Context,
	launcher *run.RunLauncher,
	params run.LaunchParams,
	concurrency model.ConcurrencyPolicy,
	queueDepth int,
	logPrefix string,
	logAttrs ...any,
) launchOutcome {
	// withAttrs prepends "policy_id" then appends the caller's extra attrs so
	// every log line carries consistent correlation fields.
	withAttrs := func(extra ...any) []any {
		attrs := make([]any, 0, 2+len(logAttrs)+len(extra))
		attrs = append(attrs, "policy_id", params.PolicyID)
		attrs = append(attrs, logAttrs...)
		attrs = append(attrs, extra...)
		return attrs
	}

	if err := launcher.CheckConcurrency(ctx, params.PolicyID, concurrency); err != nil {
		switch {
		case errors.Is(err, run.ErrConcurrencySkipActive):
			slog.Info(logPrefix+": skipping run, active run exists (concurrency: skip)", withAttrs()...)
			return outcomeSkipped
		case errors.Is(err, run.ErrConcurrencyQueueActive):
			if enqErr := launcher.Enqueue(ctx, params, queueDepth); enqErr != nil {
				if errors.Is(enqErr, run.ErrConcurrencyQueueFull) {
					slog.Warn(logPrefix+": trigger queue is full", withAttrs()...)
				} else {
					slog.Error(logPrefix+": failed to enqueue trigger", withAttrs("err", enqErr)...)
				}
				return outcomeError
			}
			slog.Info(logPrefix+": trigger queued (active run exists)", withAttrs()...)
			return outcomeQueued
		default:
			slog.Error(logPrefix+": concurrency check failed", withAttrs("err", err)...)
			return outcomeError
		}
	}

	result, err := launcher.Launch(ctx, params)
	if err != nil {
		// run_id is populated when the failure happened after the row was
		// created (tool resolution, agent construction). Operators can use it
		// to find the failed run in history, where the recorded error lives.
		slog.Error(logPrefix+": failed to launch run", withAttrs("run_id", result.RunID, "err", err)...)
		return outcomeError
	}

	slog.Info(logPrefix+": run launched", withAttrs("run_id", result.RunID)...)
	return outcomeLaunched
}
