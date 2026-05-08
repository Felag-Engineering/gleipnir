package dispatch

// RoutingOutcome reports which execution path was taken by Dispatcher.Request.
// It is a typed enum so callers can switch on it without comparing against
// sentinel errors.
type RoutingOutcome int

const (
	// RouteToPlugin means the request was dispatched to a real plugin instance
	// via gRPC and a plugin_pending_requests row was inserted.
	RouteToPlugin RoutingOutcome = iota + 1

	// RouteToInApp means the first Request-capable entry is the synthetic
	// gleipnir.in-app entry. No gRPC call is made and no plugin_pending_requests
	// row is written — the feedback_requests substrate handles in-app Requests.
	RouteToInApp
)

func (o RoutingOutcome) String() string {
	switch o {
	case RouteToPlugin:
		return "RouteToPlugin"
	case RouteToInApp:
		return "RouteToInApp"
	default:
		return "RoutingOutcome(unknown)"
	}
}
