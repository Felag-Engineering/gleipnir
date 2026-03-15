package trigger

import (
	"context"

	"github.com/rapp992/gleipnir/internal/db"
)

// CronScheduler manages cron-triggered policy runs.
// This is a stub — cron support is planned for v0.3.
//
// The scheduler will use robfig/cron to parse policy schedule expressions
// and fire runs on the configured interval. On startup it loads all policies
// with trigger_type = 'cron' from the DB and registers each one.
type CronScheduler struct {
	store    *db.Store
	launcher *RunLauncher
}

// NewCronScheduler returns a CronScheduler.
func NewCronScheduler(store *db.Store, launcher *RunLauncher) *CronScheduler {
	return &CronScheduler{
		store:    store,
		launcher: launcher,
	}
}

// Start begins executing scheduled policies. Will block until ctx is cancelled
// once implemented.
// TODO: implement
func (s *CronScheduler) Start(ctx context.Context) error {
	panic("not implemented")
}

// Stop gracefully shuts down the scheduler.
// TODO: implement
func (s *CronScheduler) Stop() error {
	panic("not implemented")
}
