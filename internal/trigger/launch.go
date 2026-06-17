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

// writeLaunchOutcome calls LaunchWithConcurrency and writes the HTTP response.
// It is the shared HTTP adapter for webhook and manual trigger handlers.
// logPrefix identifies the trigger type in log messages (e.g. "webhook").
//
// The error switch enumerates every case explicitly to ensure no error silently
// falls into the wrong branch — in particular, ErrConcurrencyUnrecognised must
// use WriteError (not WriteLaunchError) because no run row exists when
// CheckConcurrency returns it.
func writeLaunchOutcome(
	ctx context.Context,
	w http.ResponseWriter,
	launcher *run.RunLauncher,
	params run.LaunchParams,
	logPrefix string,
) {
	res, err := launcher.LaunchWithConcurrency(ctx, params)
	if err != nil {
		switch {
		case errors.Is(err, run.ErrConcurrencyQueueFull):
			// No run row exists — WriteError, not WriteLaunchError.
			httputil.WriteError(w, http.StatusTooManyRequests, "trigger queue is full", "")
		case errors.Is(err, run.ErrConcurrencyUnrecognised):
			// No run row exists (res.RunID is empty). Must use WriteError.
			httputil.WriteError(w, http.StatusInternalServerError, "unrecognised concurrency policy", "")
		case run.IsConcurrencyCheckError(err):
			// Third distinct 500: raw DB error from CheckConcurrency.
			// No run row exists → WriteError.
			slog.ErrorContext(ctx, logPrefix+": failed to check active runs",
				"policy_id", params.PolicyID, "err", err)
			httputil.WriteError(w, http.StatusInternalServerError, "failed to check active runs", "")
		case run.IsEnqueueError(err):
			// Non-queue-full enqueue DB error. No run row exists → WriteError.
			slog.ErrorContext(ctx, logPrefix+": failed to enqueue trigger",
				"policy_id", params.PolicyID, "err", err)
			httputil.WriteError(w, http.StatusInternalServerError, "failed to enqueue trigger", "")
		default:
			// Launch failure. res.RunID may be set → WriteLaunchError so the
			// created-but-failed run row is deep-linkable.
			slog.ErrorContext(ctx, logPrefix+": failed to launch run",
				"policy_id", params.PolicyID, "run_id", res.RunID, "err", err)
			httputil.WriteLaunchError(w, http.StatusInternalServerError,
				"failed to launch run", err.Error(), res.RunID)
		}
		return
	}

	switch res.Outcome {
	case run.OutcomeSkipped:
		httputil.WriteError(w, http.StatusConflict,
			"run already active for this policy (concurrency: skip)", "")
	case run.OutcomeQueued:
		httputil.WriteJSON(w, http.StatusAccepted, map[string]any{"queued": true})
	case run.OutcomeLaunched:
		httputil.WriteJSON(w, http.StatusAccepted, map[string]string{"run_id": res.RunID})
	}
}
