package trigger

// CronScheduler manages cron-triggered policy runs.
// This is a stub — cron support is planned for v0.3.
//
// The scheduler will use robfig/cron to parse policy schedule expressions
// and fire runs on the configured interval. On startup it loads all policies
// with trigger_type = 'cron' from the DB and registers each one.
type CronScheduler struct{}

// NewCronScheduler returns a CronScheduler.
// TODO: accept store and agent runner as dependencies.
func NewCronScheduler() *CronScheduler {
	return &CronScheduler{}
}

// Start begins executing scheduled policies. Blocks until ctx is cancelled.
// TODO: implement
func (s *CronScheduler) Start() {
	panic("not implemented")
}

// Stop gracefully shuts down the scheduler.
// TODO: implement
func (s *CronScheduler) Stop() {
	panic("not implemented")
}
