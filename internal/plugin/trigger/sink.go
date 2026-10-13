//go:build !substratev2

// This file is the v1.1 gRPC half of the package: it imports the go-plugin
// subprocess manager (and, for sink.go, hostsvc), which the substratev2 build
// must not link. The v2 assembly still needs Dispatcher and ListenSinkAdapter
// from this package, so the v1-only files are excluded from that build rather
// than the shared ones being moved.

package trigger

import (
	"context"

	"github.com/felag-engineering/gleipnir/internal/plugin/hostsvc"
)

// SinkAdapter wraps a *Dispatcher so it satisfies hostsvc.TriggerSink.
// hostsvc cannot import this package directly (it would create a cycle if
// this package ever imported hostsvc), so the adapter is declared here and
// wired in main.go via hostsvc.SetTriggerSink.
type SinkAdapter struct {
	d *Dispatcher
}

// NewSinkAdapter returns a SinkAdapter that forwards EmittedEvent to the
// given Dispatcher after converting the hostsvc event type to the local one.
func NewSinkAdapter(d *Dispatcher) *SinkAdapter {
	return &SinkAdapter{d: d}
}

// Handle satisfies hostsvc.TriggerSink. It converts the hostsvc.EmittedEvent
// to the package-local Event type and delegates to Dispatcher.Handle.
func (a *SinkAdapter) Handle(ctx context.Context, e hostsvc.EmittedEvent) error {
	return a.d.Handle(ctx, Event{
		InstanceID:  e.InstanceID,
		PluginID:    e.PluginID,
		EventKind:   e.EventKind,
		EventID:     e.EventID,
		PayloadJSON: e.PayloadJSON,
		ObservedAt:  e.ObservedAt,
	})
}
