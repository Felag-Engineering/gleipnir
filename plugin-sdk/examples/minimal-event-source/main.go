// Command minimal-event-source is the smallest possible io.gleipnir/events
// plugin: it declares a single event kind, "example.ping", and emits one
// every publishInterval for as long as the process runs.
//
// Unlike minimal-tool (a go-plugin subprocess launched over the v1.1 gRPC
// handshake), an event-source plugin under the MCP realignment (ADR-053) is
// a containerized MCP server the host reaches over streamable-HTTP — so
// this example's main() just serves plugin-sdk/events.Handler on a port,
// the same shape a real event-source plugin's container entrypoint would
// have.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/felag-engineering/gleipnir/plugin-sdk/events"
)

// eventKinds is declared once and handed to events.NewHandler below. It is
// also what manifest_test.go compares manifest.yaml's event_kinds against —
// see that file for why the two cannot drift apart silently.
var eventKinds = []events.Kind{
	{
		Kind:     "example.ping",
		Guidance: "Fires once per publish, on this example's own ticker loop.",
	},
}

func main() {
	handler := events.NewHandler("minimal-event-source", eventKinds)
	go publishPings(handler)

	const addr = ":8080"
	log.Printf("minimal-event-source listening on %s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("minimal-event-source: %v", err)
	}
}

// publishPings is the example's own stand-in for a real event source (a
// webhook receiver, a polled API, a device feed, ...): it manufactures one
// "example.ping" event per tick so the handler has something to deliver.
func publishPings(handler *events.Handler) {
	const publishInterval = 30 * time.Second
	ticker := time.NewTicker(publishInterval)
	defer ticker.Stop()

	for i := 0; ; i++ {
		<-ticker.C
		_, err := handler.Publish(context.Background(), events.Event{
			Type: "example.ping",
			ID:   fmt.Sprintf("ping-%d", i),
			Data: map[string]any{"sequence": i},
		})
		if err != nil {
			log.Printf("minimal-event-source: publish: %v", err)
		}
	}
}
