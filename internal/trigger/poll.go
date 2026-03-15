package trigger

import (
	"context"

	"github.com/rapp992/gleipnir/internal/db"
)

// PollEngine executes poll-triggered policies on a configured interval.
// This is a stub — poll support is planned for v0.3.
//
// The engine will evaluate a JSONPath filter against an HTTP endpoint response
// and fire a run when the filter returns a non-empty result. The matched result
// becomes the trigger payload delivered to the agent.
type PollEngine struct {
	store    *db.Store
	launcher *RunLauncher
}

// NewPollEngine returns a PollEngine.
func NewPollEngine(store *db.Store, launcher *RunLauncher) *PollEngine {
	return &PollEngine{
		store:    store,
		launcher: launcher,
	}
}

// Start begins polling all active poll-triggered policies. Will block until ctx
// is cancelled once implemented.
// TODO: implement
func (e *PollEngine) Start(ctx context.Context) error {
	panic("not implemented")
}

// Stop gracefully shuts down the poll engine.
// TODO: implement
func (e *PollEngine) Stop() error {
	panic("not implemented")
}
