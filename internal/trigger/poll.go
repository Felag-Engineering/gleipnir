package trigger

// PollEngine executes poll-triggered policies on a configured interval.
// This is a stub — poll support is planned for v0.3.
//
// The engine will evaluate a JSONPath filter against an HTTP endpoint response
// and fire a run when the filter returns a non-empty result. The matched result
// becomes the trigger payload delivered to the agent.
type PollEngine struct{}

// NewPollEngine returns a PollEngine.
// TODO: accept store and agent runner as dependencies.
func NewPollEngine() *PollEngine {
	return &PollEngine{}
}

// Start begins polling all active poll-triggered policies. Blocks until ctx
// is cancelled.
// TODO: implement
func (e *PollEngine) Start() {
	panic("not implemented")
}

// Stop gracefully shuts down the poll engine.
// TODO: implement
func (e *PollEngine) Stop() {
	panic("not implemented")
}
