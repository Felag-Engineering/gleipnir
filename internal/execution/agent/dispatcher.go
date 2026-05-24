package agent

import "context"

// PluginToolDispatcher is the interface the agent uses to invoke plugin-backed
// tools. internal/plugin/dispatch.Pool satisfies it structurally; the small
// adapter in main.go bridges the two without importing dispatch from agent.
//
// Error sentinels (ErrPluginCallTimeout, ErrPluginQueueFull) are aliases of
// the same values returned by dispatch.Pool — both reference
// internal/plugin/pluginerr — so errors.Is works without any translation
// layer in the adapter.
type PluginToolDispatcher interface {
	Call(ctx context.Context, runID, policyID, instanceName, toolName, inputJSON string) (output string, isError bool, err error)
}
