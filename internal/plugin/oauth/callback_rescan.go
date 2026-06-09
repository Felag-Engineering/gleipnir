package oauth

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
)

// RescanQuerier is the narrow DB interface needed by CallbackRescanner.
// Using an interface (not *db.Queries) keeps the struct testable with fakes.
type RescanQuerier interface {
	ListPluginInstancesForCallbackRescan(ctx context.Context) ([]db.PluginInstance, error)
}

// CallbackRescanner compares each eligible plugin instance's
// last_oauth_callback_url against the current OAuth callback URL derived from
// public_url. Instances whose recorded URL no longer matches are transitioned
// to PluginHealthStatePendingReauthorize so operators can re-run the OAuth flow.
//
// The rescan is triggered once after a public_url change rather than on a timer,
// so it is intentionally synchronous and inexpensive.
type CallbackRescanner struct {
	q            RescanQuerier
	health       pluginstate.Querier
	getPublicURL func() string
	clock        func() time.Time
}

// NewCallbackRescanner constructs a CallbackRescanner. clock may be nil (defaults
// to time.Now). getPublicURL is called at scan time to reflect late-bound settings.
func NewCallbackRescanner(q RescanQuerier, health pluginstate.Querier, getPublicURL func() string, clock func() time.Time) *CallbackRescanner {
	if clock == nil {
		clock = time.Now
	}
	return &CallbackRescanner{
		q:            q,
		health:       health,
		getPublicURL: getPublicURL,
		clock:        clock,
	}
}

// Scan iterates over eligible plugin instances and transitions those whose
// last_oauth_callback_url no longer matches the expected callback URL to
// PluginHealthStatePendingReauthorize.
//
// Returns the number of instances flagged and any DB-level error from the list
// query. Individual transition errors (ErrIllegalTransition, ErrTransitionConflict)
// are logged as warnings and do not stop the scan.
func (r *CallbackRescanner) Scan(ctx context.Context) (flagged int, err error) {
	publicURL := r.getPublicURL()
	if publicURL == "" {
		slog.DebugContext(ctx, "callback rescan: public_url not set; skipping")
		return 0, nil
	}

	expected := publicURL + callbackPath

	rows, err := r.q.ListPluginInstancesForCallbackRescan(ctx)
	if err != nil {
		return 0, fmt.Errorf("callback rescan: list instances: %w", err)
	}

	for _, row := range rows {
		if row.LastOauthCallbackUrl == nil || *row.LastOauthCallbackUrl == expected {
			continue
		}

		detail := fmt.Sprintf(
			"public_url changed: recorded callback %q no longer matches current %q; re-authorize to update",
			*row.LastOauthCallbackUrl, expected,
		)
		err := pluginstate.SetHealthState(
			ctx, r.health, nil, row.ID,
			pluginstate.OriginHost,
			model.PluginHealthStatePendingReauthorize,
			detail,
		)
		if err != nil {
			// ErrIllegalTransition means the instance is in a state that cannot
			// transition to pending_reauthorize (e.g. already pending_reauthorize,
			// or in a terminal/error state). ErrTransitionConflict means a concurrent
			// writer already advanced the version. Both are benign — log and move on.
			slog.WarnContext(ctx, "callback rescan: set pending_reauthorize",
				"instance_id", row.ID,
				"plugin_id", row.PluginID,
				"err", err,
			)
			continue
		}
		flagged++
	}

	if flagged > 0 {
		slog.InfoContext(ctx, "callback rescan: flagged instances for re-authorization",
			"count", flagged,
			"new_callback_url", expected,
		)
	}

	return flagged, nil
}
