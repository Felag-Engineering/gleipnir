package assembly

import (
	"context"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/dedup"
)

// timeNow is the package's injectable clock (CLAUDE.md "Testing time-dependent
// code"). Tests swap it via t.Cleanup and must not call t.Parallel().
var timeNow = func() time.Time { return time.Now() }

const (
	// nonceJanitorInterval is how often expired OAuth state nonces are pruned.
	nonceJanitorInterval = time.Minute

	// taskPollTick is the housekeeping cadence of the Tasks poll scheduler: how
	// often it checks whether any open task is due, not how often a task is
	// polled (each server declares its own cadence). It only needs to be
	// shorter than the shortest realistic per-task interval.
	taskPollTick = 5 * time.Second

	// containerGCInterval is how often the image/subnet/token cleanup pass
	// runs. Cleanup is deliberately slower than convergence: nothing gets
	// worse for an hour because a dead image is still on disk.
	containerGCInterval = time.Hour
)

// backgroundLoop is a blocking function the assembly runs on its bg
// WaitGroup. It must return promptly once ctx is cancelled.
type backgroundLoop struct {
	name string
	run  func(ctx context.Context)
}

// newBackgroundLoops builds the scanners the v1 substrate ran under
// pluginRuntime.bgWG: the dedup sweeper, and (only when an encryption key
// makes credential storage possible) the OAuth nonce janitor and token refresh
// scanner. They are constructed here and started by startBackground, so New
// still starts nothing.
func newBackgroundLoops(deps Deps, q *db.Queries, creds *credentialParts) []backgroundLoop {
	sweeper := dedup.NewSweeper(q, deps.Config.PluginDedupSweepInterval, dedup.TTL)
	loops := []backgroundLoop{{name: "dedup sweeper", run: sweeper.Start}}

	if creds == nil {
		return loops
	}
	return append(loops,
		backgroundLoop{name: "oauth nonce janitor", run: func(ctx context.Context) {
			creds.nonces.StartJanitor(ctx, nonceJanitorInterval)
		}},
		backgroundLoop{name: "oauth refresh scanner", run: creds.scanner.Run},
	)
}

// startBackground launches every composed loop on the bg WaitGroup.
func (a *Assembly) startBackground() {
	for _, l := range a.backgrounds {
		a.goBackground(l.run)
	}
}

// goBackground runs fn on the bg WaitGroup so Shutdown joins it rather than
// abandoning it mid-DB-write.
func (a *Assembly) goBackground(fn func(ctx context.Context)) {
	a.bg.Add(1)
	go func() {
		defer a.bg.Done()
		fn(a.ctx)
	}()
}

// every returns a loop that calls fn once per interval until ctx is cancelled.
// The first call is one interval after the loop starts, not immediately.
func every(interval time.Duration, fn func(ctx context.Context)) func(ctx context.Context) {
	return func(ctx context.Context) {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fn(ctx)
			}
		}
	}
}
