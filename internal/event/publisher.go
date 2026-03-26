package event

import "encoding/json"

// Publisher emits real-time events (e.g. run status changes, new steps).
// Implemented by sse.Broadcaster; accepted as an injected dependency so
// callers never import internal/sse directly.
type Publisher interface {
	Publish(eventType string, data json.RawMessage)
}
