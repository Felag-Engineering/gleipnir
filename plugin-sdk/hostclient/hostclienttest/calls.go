package hostclienttest

import (
	"context"
	"encoding/json"
	"fmt"
)

// Call is one host-endpoint invocation the fake successfully dispatched to a
// handler. A JSON-RPC transport fault (bad headers, unknown method) never
// reaches this log — exactly as it never reaches a handler on the real
// endpoint (contract §2.2): the log records calls, not attempts.
type Call struct {
	// Method is "server/discover", "tools/list", or the called tool's name
	// (e.g. "host/log") for a tools/call.
	Method string
	// Arguments is the raw tools/call arguments object. Empty for
	// server/discover and tools/list.
	Arguments json.RawMessage
}

// LogEntry is one recorded host/log call.
type LogEntry struct {
	Level string
	Msg   string
	Attrs map[string]string
}

// MetricEntry is one recorded host/emit_metric call. Unlike the real
// endpoint, Name is recorded verbatim with no forced gleipnir_plugin_ prefix
// and no cardinality cap (doc.go's divergence note).
type MetricEntry struct {
	Name   string
	Value  float64
	Labels map[string]string
}

// HealthEntry is one recorded host/set_health_state call, including whether
// it was applied under the §8.1 worsen-only rule.
type HealthEntry struct {
	Profile    string
	Capability string
	State      string
	Detail     string
	Applied    bool
}

// Calls returns every call the fake has dispatched to a handler so far, in
// arrival order.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Call, len(s.calls))
	copy(out, s.calls)
	return out
}

// Logs returns every recorded host/log call, in arrival order.
func (s *Server) Logs() []LogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LogEntry, len(s.logs))
	copy(out, s.logs)
	return out
}

// Metrics returns every recorded host/emit_metric call, in arrival order.
func (s *Server) Metrics() []MetricEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MetricEntry, len(s.metrics))
	copy(out, s.metrics)
	return out
}

// HealthReports returns every recorded host/set_health_state call, in
// arrival order.
func (s *Server) HealthReports() []HealthEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]HealthEntry, len(s.health))
	copy(out, s.health)
	return out
}

// recordCall appends c to the call log and wakes any WaitForCall waiters
// blocked on c.Method.
func (s *Server) recordCall(c Call) {
	s.mu.Lock()
	s.calls = append(s.calls, c)
	waiters := s.waiters[c.Method]
	delete(s.waiters, c.Method)
	s.mu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

// WaitForCall blocks until a call to method (a tool name like "host/log", or
// "server/discover" / "tools/list") has been recorded, then returns it.
// Signal-don't-poll: it returns immediately if the call already arrived
// before WaitForCall was called, and otherwise blocks on a channel the next
// matching call closes — no wall-clock polling. ctx bounds the wait.
func (s *Server) WaitForCall(ctx context.Context, method string) (Call, error) {
	s.mu.Lock()
	for _, c := range s.calls {
		if c.Method == method {
			s.mu.Unlock()
			return c, nil
		}
	}
	ch := make(chan struct{})
	s.waiters[method] = append(s.waiters[method], ch)
	s.mu.Unlock()

	select {
	case <-ch:
		s.mu.Lock()
		defer s.mu.Unlock()
		for i := len(s.calls) - 1; i >= 0; i-- {
			if s.calls[i].Method == method {
				return s.calls[i], nil
			}
		}
		// recordCall only closes ch after appending the matching call, so
		// this is unreachable outside a logic error in this file.
		return Call{}, fmt.Errorf("hostclienttest: WaitForCall(%q): call recorded but not found", method)
	case <-ctx.Done():
		return Call{}, ctx.Err()
	}
}
