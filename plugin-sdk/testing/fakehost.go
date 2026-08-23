// Package testing provides the fake host and test harness for Gleipnir plugin
// authors. See doc.go for the full package overview.
//
// Import alias: consumers should import this package as:
//
//	plugintest "github.com/felag-engineering/gleipnir/plugin-sdk/testing"
//
// to avoid shadowing the stdlib "testing" package in test files.
package testing

import (
	"google.golang.org/grpc"

	"github.com/felag-engineering/gleipnir/plugin-sdk/internal/fakehost"
)

// FakeHost is an in-process implementation of the Gleipnir host gRPC service
// that records every RPC call for inspection. Create with NewFakeHost.
//
// No method on FakeHost returns a proto type from gen/.../hostv1 — all
// accessors return local types defined in types.go.
type FakeHost struct {
	inner *fakehost.Host
	cfg   *config
}

// NewFakeHost creates a FakeHost with the given options applied.
// Options are evaluated in order; later options override earlier ones.
func NewFakeHost(opts ...Option) *FakeHost {
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}
	return &FakeHost{
		inner: fakehost.New(toFakehostOptions(cfg)),
		cfg:   cfg,
	}
}

// Register registers the fake host as the HostService implementation on srv.
// Call this before starting the gRPC server in your test.
func (f *FakeHost) Register(srv *grpc.Server) {
	f.inner.Register(srv)
}

// Metrics returns all EmitMetric requests received, projected to local Metric
// values.
func (f *FakeHost) Metrics() []Metric {
	raw := f.inner.Metrics()
	out := make([]Metric, len(raw))
	for i, r := range raw {
		out[i] = fromProtoMetric(r)
	}
	return out
}

// Events returns all EmitEvent requests received, projected to local Event
// values.
func (f *FakeHost) Events() []Event {
	raw := f.inner.Events()
	out := make([]Event, len(raw))
	for i, r := range raw {
		out[i] = fromProtoEvent(r)
	}
	return out
}

// Logs returns all Log requests received, projected to local LogLine values.
func (f *FakeHost) Logs() []LogLine {
	raw := f.inner.Logs()
	out := make([]LogLine, len(raw))
	for i, r := range raw {
		out[i] = fromProtoLog(r)
	}
	return out
}

// Health returns the most recent SetHealthState call as a local HealthState
// constant. ok is false when no health state has been reported yet.
func (f *FakeHost) Health() (state HealthState, detail string, ok bool) {
	req := f.inner.HealthStates()
	if req == nil {
		return HealthStateUnspecified, "", false
	}
	s, d := fromProtoHealth(req)
	return s, d, true
}

// RunHistoryCalls returns the number of RunHistoryRead RPCs received since
// the last Reset (or since creation). Only incremented when canned data is
// configured via WithRunHistory.
func (f *FakeHost) RunHistoryCalls() int {
	return f.inner.RunHistoryCalls()
}

// UserDirectoryCalls returns the number of UserDirectoryRead RPCs received
// since the last Reset (or since creation). Only incremented when canned data
// is configured via WithUserDirectory.
func (f *FakeHost) UserDirectoryCalls() int {
	return f.inner.UserDirectoryCalls()
}

// Reset clears all recorded calls (metrics, events, logs, health state, and
// Tier-2 call counts). The configured options (canned data, callbacks, etc.)
// are unchanged.
func (f *FakeHost) Reset() {
	f.inner.Reset()
}
