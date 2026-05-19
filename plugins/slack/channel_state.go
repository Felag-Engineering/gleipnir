package main

import (
	"sync"
	"time"
)

// correlation holds the state associated with an in-flight Request.
// It is stored in correlationMap keyed by the request_id returned by the host.
//
// Restart loses all correlations; in-flight Slack messages resolve via the
// host's feedback-timeout path (spec §4.2 lines 102, 106).
// RecoverChannelRequests is v2.
type correlation struct {
	channel string
	ts      string
	buttons []responseButton
	runID   string
	addedAt time.Time
}

// correlationMap stores in-flight Request correlations. Safe for concurrent use.
type correlationMap struct {
	mu   sync.Mutex
	data map[string]correlation

	stopCh chan struct{}
}

// newCorrelationMap creates a correlationMap and starts a background sweep
// goroutine that evicts entries older than ttl every interval.
func newCorrelationMap(interval, ttl time.Duration) *correlationMap {
	m := &correlationMap{
		data:   make(map[string]correlation),
		stopCh: make(chan struct{}),
	}
	go m.sweepLoop(interval, ttl)
	return m
}

// put stores a correlation for requestID.
func (m *correlationMap) put(requestID string, c correlation) {
	m.mu.Lock()
	m.data[requestID] = c
	m.mu.Unlock()
}

// take atomically retrieves and deletes the correlation for requestID.
// Returns the correlation and true if found; zero value and false otherwise.
func (m *correlationMap) take(requestID string) (correlation, bool) {
	m.mu.Lock()
	c, ok := m.data[requestID]
	if ok {
		delete(m.data, requestID)
	}
	m.mu.Unlock()
	return c, ok
}

// sweep evicts all entries older than ttl as of now.
// Returns the number of entries evicted.
func (m *correlationMap) sweep(now time.Time, ttl time.Duration) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	var evicted int
	for id, c := range m.data {
		if now.Sub(c.addedAt) > ttl {
			delete(m.data, id)
			evicted++
		}
	}
	return evicted
}

// sweepLoop runs sweep on the given interval until Stop is called.
func (m *correlationMap) sweepLoop(interval, ttl time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.sweep(time.Now(), ttl)
		case <-m.stopCh:
			return
		}
	}
}

// Stop halts the background sweep goroutine. Intended for test cleanup;
// production processes exit naturally.
func (m *correlationMap) Stop() {
	close(m.stopCh)
}
