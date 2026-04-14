package trigger

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/httputil"
	"github.com/rapp992/gleipnir/internal/model"
	"github.com/rapp992/gleipnir/internal/policy"
	"github.com/rapp992/gleipnir/internal/run"
)

// fetchAndParsePolicy loads a policy by ID and parses its YAML.
// On failure it writes the appropriate HTTP error and returns nil.
func fetchAndParsePolicy(ctx context.Context, w http.ResponseWriter, store *db.Store, policyID string) *model.ParsedPolicy {
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

	parsed, err := policy.Parse(dbPolicy.Yaml, model.DefaultProvider, model.DefaultModelName)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "failed to parse policy", "")
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
		httputil.WriteError(w, http.StatusInternalServerError, "failed to launch run", "")
		return
	}

	httputil.WriteJSON(w, http.StatusAccepted, map[string]string{"run_id": result.RunID})
}
