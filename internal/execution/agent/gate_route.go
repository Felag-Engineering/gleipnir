package agent

// GateRoute records where an open approval or feedback gate is actually
// waiting for its answer. It is the route in effect right now, not the one the
// policy's audience would normally pick: a request that fell back to in-app
// (unresolvable audience, no Request-capable entry, no dispatcher) is in-app
// regardless of what the policy configures.
type GateRoute struct {
	// Plugin is true while the request is held by a plugin channel. That
	// channel's own answer path is the only one that can settle it; an in-app
	// answer that won the approval/feedback CAS first would make the plugin's
	// later answer lose it and fail the run.
	Plugin bool

	// InstanceID is the plugin instance holding the request. Empty while the
	// route is still being resolved (Plugin is set before dispatch, since the
	// dispatcher only learns the chosen entry partway through).
	InstanceID string
}

// GateRouteRecorder is how a gate's handler tells the run-tracking layer where
// its open request is waiting. internal/execution/run.RunManager implements it;
// the interface lives here so agent does not import that package.
type GateRouteRecorder interface {
	RecordGateRoute(runID string, route GateRoute)
	ClearGateRoute(runID string)
}

// pluginRouteRecorder returns the OnPluginRoute callback for a dispatch
// request, or nil when routes are not tracked.
func pluginRouteRecorder(r GateRouteRecorder, runID string) func(instanceID string) {
	if r == nil {
		return nil
	}
	return func(instanceID string) {
		r.RecordGateRoute(runID, GateRoute{Plugin: true, InstanceID: instanceID})
	}
}
