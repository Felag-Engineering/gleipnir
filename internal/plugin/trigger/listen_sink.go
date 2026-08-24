package trigger

import (
	"context"

	"github.com/felag-engineering/gleipnir/internal/plugin/events"
)

// ListenSinkAdapter wraps a *Dispatcher so it satisfies events.Sink,
// mirroring SinkAdapter's role for hostsvc.TriggerSink. internal/plugin/events
// must not import this package (see its package doc), so the adapter lives
// here and is wired into an events.Supervisor's Config.Sink wherever that
// supervisor is constructed.
//
// This is what makes spec §5's pipeline commitment literally true for the
// events/listen ingestion path: "Delivery flows into the existing pipeline
// unchanged" — both this adapter and the v1.1 SinkAdapter end at the exact
// same *Dispatcher.Handle.
type ListenSinkAdapter struct {
	d *Dispatcher
}

// NewListenSinkAdapter returns a ListenSinkAdapter that forwards events.Event
// to the given Dispatcher after converting it to the local Event type.
func NewListenSinkAdapter(d *Dispatcher) *ListenSinkAdapter {
	return &ListenSinkAdapter{d: d}
}

// Handle satisfies events.Sink. It converts the events.Event to the
// package-local Event type and delegates to Dispatcher.Handle.
func (a *ListenSinkAdapter) Handle(ctx context.Context, e events.Event) error {
	return a.d.Handle(ctx, Event{
		InstanceID:  e.InstanceID,
		PluginID:    e.PluginID,
		EventKind:   e.EventKind,
		EventID:     e.EventID,
		PayloadJSON: e.PayloadJSON,
		ObservedAt:  e.ObservedAt,
	})
}
