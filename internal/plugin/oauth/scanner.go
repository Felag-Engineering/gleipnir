package oauth

import (
	"context"
	"log/slog"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// ScannerQuerier is the narrow DB interface used by RefreshScanner.
type ScannerQuerier interface {
	ListPluginInstancesWithExpiringCredentials(ctx context.Context, cutoff *string) ([]db.PluginInstance, error)
}

// RefreshScanner proactively refreshes OAuth2 tokens for plugin instances whose
// credentials are approaching expiry. It runs on a configurable interval and
// queries for instances expiring within a configurable lead window.
//
// The scanner mirrors the approval/feedback timeout scanners in structure
// (Start → background goroutine → tick loop).
type RefreshScanner struct {
	store        *DBStore
	q            ScannerQuerier
	getPublicURL func() string
	interval     time.Duration
	lead         time.Duration
	timeNow      func() time.Time // injectable for tests; do not call t.Parallel in tests that swap this
}

// NewRefreshScanner constructs a RefreshScanner. interval controls how often
// the scanner runs; lead controls how far in advance of expiry a token is
// refreshed (e.g. 15m means tokens expiring in the next 15 minutes are
// refreshed now).
func NewRefreshScanner(store *DBStore, q ScannerQuerier, getPublicURL func() string, interval, lead time.Duration) *RefreshScanner {
	return &RefreshScanner{
		store:        store,
		q:            q,
		getPublicURL: getPublicURL,
		interval:     interval,
		lead:         lead,
		timeNow:      time.Now,
	}
}

// Start launches the background refresh goroutine. It runs until ctx is
// cancelled and is designed to be called once at startup (analogous to
// approvalScanner.Start).
//
// Prefer Run when the caller needs a join point on shutdown (it blocks until
// ctx is cancelled and the in-flight scan finishes). Start is the fire-and-forget
// variant retained for callers that do not join the goroutine.
func (rs *RefreshScanner) Start(ctx context.Context) {
	go rs.run(ctx)
}

// Run executes the refresh loop synchronously, returning only when ctx is
// cancelled (after any in-flight scan tick completes). It is the blocking
// counterpart to Start so a caller can own the goroutine and join it on
// shutdown — see pluginruntime.go's bgWG wiring (#500).
func (rs *RefreshScanner) Run(ctx context.Context) {
	rs.run(ctx)
}

func (rs *RefreshScanner) run(ctx context.Context) {
	ticker := time.NewTicker(rs.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rs.scan(ctx)
		}
	}
}

func (rs *RefreshScanner) scan(ctx context.Context) {
	cutoff := rs.timeNow().Add(rs.lead).UTC().Format(time.RFC3339Nano)
	rows, err := rs.q.ListPluginInstancesWithExpiringCredentials(ctx, &cutoff)
	if err != nil {
		slog.ErrorContext(ctx, "oauth scanner: query expiring credentials failed", "err", err)
		return
	}

	publicURL := rs.getPublicURL()
	skippedAuthcode := false

	for _, row := range rows {
		creds, _, err := rs.store.LoadCredentials(ctx, row.ID)
		if err != nil {
			slog.ErrorContext(ctx, "oauth scanner: load credentials failed", "instance_id", row.ID, "err", err)
			continue
		}

		// For authcode instances, the public_url must be set because it is
		// embedded in the oauth2.Config.RedirectURL. Client credentials flows
		// do not make a redirect URL — they can be refreshed without it.
		if creds.Strategy == sdkmanifest.AuthStrategyOAuth2Authcode && publicURL == "" {
			if !skippedAuthcode {
				slog.WarnContext(ctx, "oauth scanner: public_url not configured; skipping authcode refresh for this tick")
				skippedAuthcode = true
			}
			continue
		}

		callbackURL := publicURL + callbackPath
		ts := NewTokenSource(ctx, rs.store, row.ID, callbackURL, rs.timeNow)
		tok, err := ts.Token()
		if err != nil {
			// MarkRefreshFailed was already called inside Token(); just log.
			slog.ErrorContext(ctx, "oauth scanner: refresh failed", "instance_id", row.ID, "err", err)
			continue
		}
		slog.InfoContext(ctx, "oauth scanner: token refreshed", "instance_id", row.ID, "expiry", tok.Expiry)
	}
}
