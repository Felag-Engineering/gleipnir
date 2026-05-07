package agent

import "context"

// PluginToolDispatcher is the interface the agent uses to invoke plugin-backed
// tools. internal/plugin/dispatch.Pool satisfies it structurally.
//
// This indirection preserves the package boundary: internal/execution/agent
// must NOT import internal/plugin/dispatch. The translation between dispatch-
// package sentinel errors (dispatch.ErrCallTimeout, dispatch.ErrQueueFull) and
// the agent-package sentinels (ErrPluginCallTimeout, ErrPluginQueueFull) lives
// in the adapter wired in main.go.
type PluginToolDispatcher interface {
	Call(ctx context.Context, runID, policyID, instanceName, toolName, inputJSON string) (output string, isError bool, err error)
}
