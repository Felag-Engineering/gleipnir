package agent

import "encoding/json"

// Publisher emits real-time events. Implemented by the SSE broadcaster;
// accepted as injected dependency so agent never imports internal/sse.
type Publisher interface {
	Publish(eventType string, data json.RawMessage)
}
