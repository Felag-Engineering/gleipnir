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
)

// defaultModelResolver fetches the system-wide default LLM provider and model
// name from persistent storage. *admin.Handler satisfies this interface.
type defaultModelResolver interface {
	GetSystemDefault(ctx context.Context) (provider string, modelName string, err error)
}

// fetchAndParsePolicy loads a policy by ID, resolves the system default model
// (used when the policy YAML omits the model block), and parses the YAML.
// If the resolved model is empty and the policy also omits the model block the
// run cannot proceed; the handler writes a 500 and returns nil.
// On any other failure it writes the appropriate HTTP error and returns nil.
func fetchAndParsePolicy(ctx context.Context, w http.ResponseWriter, store *db.Store, policyID string, resolver defaultModelResolver) *model.ParsedPolicy {
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

	provider, modelName, err := resolver.GetSystemDefault(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to load system default model", "")
		return nil
	}
	// sql.ErrNoRows means no system default is configured — pass ("", "") so
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
