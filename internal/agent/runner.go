package agent

import "context"

// Runner is the interface satisfied by any agent implementation that can
// execute a policy run. BoundAgent is the sole implementation.
type Runner interface {
	Run(ctx context.Context, runID string, triggerPayload string) error
}

// Compile-time assertion: *BoundAgent must satisfy Runner.
// If BoundAgent.Run ever drifts from the interface, this line breaks the build.
var _ Runner = (*BoundAgent)(nil)
