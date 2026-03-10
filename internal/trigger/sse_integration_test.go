package trigger_test

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/rapp992/gleipnir/internal/agent"
	"github.com/rapp992/gleipnir/internal/sse"
	"github.com/rapp992/gleipnir/internal/trigger"
)

// sseEvent holds the parsed fields of a single SSE event.
type sseEvent struct {
	ID   string
	Type string
	Data string
}

// buildSSERouter wires a WebhookHandler (with broadcaster as publisher), a
// RunsHandler, and the SSE handler together into a single chi router.
// It inserts integrationPolicy under policyID into a fresh store.
func buildSSERouter(t *testing.T, policyID string, msgs agent.MessagesAPI, broadcaster *sse.Broadcaster) (http.Handler, *trigger.RunManager) {
	t.Helper()
	store, registry := setupIntegrationFixture(t)
	insertTestPolicy(t, store, policyID, integrationPolicy)

	manager := trigger.NewRunManager()
	factory := trigger.AgentFactory(func(cfg agent.Config) (*agent.BoundAgent, error) {
		cfg.MessagesOverride = msgs
		return agent.New(cfg)
	})
	wh := trigger.NewWebhookHandler(store, registry, manager, factory, broadcaster)
	rh := trigger.NewRunsHandler(store, manager)

	r := newRunsRouter(rh)
	r.Post("/api/v1/webhooks/{policyID}", wh.Handle)
	r.Get("/api/v1/events", sse.NewHandler(broadcaster).ServeHTTP)
	return r, manager
}

// connectSSE opens a GET /api/v1/events request against srvURL. When
// lastEventID is non-empty it is sent as the Last-Event-ID header.
func connectSSE(t *testing.T, srvURL, lastEventID string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srvURL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("connectSSE NewRequest: %v", err)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connectSSE Do: %v", err)
	}
	return resp
}

// drainSSEEvents reads the SSE stream until n events are collected or timeout
// elapses. It parses id/event/data field lines and groups them into sseEvent
// structs using blank lines as delimiters.
func drainSSEEvents(t *testing.T, resp *http.Response, n int, timeout time.Duration) []sseEvent {
	t.Helper()
	eventCh := make(chan sseEvent)

	go func() {
		scanner := bufio.NewScanner(resp.Body)
		var cur sseEvent
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				cur.ID = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				cur.Type = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				cur.Data = strings.TrimPrefix(line, "data: ")
			case line == "":
				// Blank line delimits one complete event.
				if cur.Type != "" || cur.ID != "" {
					eventCh <- cur
					cur = sseEvent{}
				}
			}
		}
		close(eventCh)
	}()

	var events []sseEvent
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for len(events) < n {
		select {
		case ev, ok := <-eventCh:
			if !ok {
				t.Logf("drainSSEEvents: stream closed after %d/%d events", len(events), n)
				return events
			}
			events = append(events, ev)
		case <-timer.C:
			t.Logf("drainSSEEvents: timed out after collecting %d/%d events", len(events), n)
			return events
		}
	}
	return events
}

// sseOneTurnMsgs returns a fake MessagesAPI that drives one tool call
// then ends the run. It is intentionally a new allocation per call so
// concurrent tests don't share state.
func sseOneTurnMsgs() *integrationFakeMessages {
	return &integrationFakeMessages{
		responses: []*anthropic.Message{
			makeToolUseMsg("tu-sse", "stub-server.read_data", map[string]any{}),
			makeTextMsg("Done."),
		},
	}
}

func TestSSEIntegration_LiveEventDelivery(t *testing.T) {
	broadcaster := sse.NewBroadcaster()
	router, manager := buildSSERouter(t, "sse-live-pol", sseOneTurnMsgs(), broadcaster)

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	resp := connectSSE(t, srv.URL, "")
	t.Cleanup(func() { resp.Body.Close() })

	// Fire the webhook after establishing the SSE connection so the
	// subscriber is registered before any events are published.
	fireWebhook(t, router, "sse-live-pol")

	// Collect events; a complete run produces status changes + several steps.
	events := drainSSEEvents(t, resp, 4, 10*time.Second)
	manager.Wait()

	if len(events) < 2 {
		t.Fatalf("expected at least 2 SSE events, got %d: %v", len(events), events)
	}

	hasStatusChanged := false
	hasStepAdded := false
	for _, ev := range events {
		switch ev.Type {
		case "run.status_changed":
			hasStatusChanged = true
		case "run.step_added":
			hasStepAdded = true
		}
	}
	if !hasStatusChanged {
		t.Errorf("no run.status_changed event received; got: %v", events)
	}
	if !hasStepAdded {
		t.Errorf("no run.step_added event received; got: %v", events)
	}

	// IDs must be non-empty and monotonically increasing.
	var prevID uint64
	for i, ev := range events {
		if ev.ID == "" {
			t.Errorf("events[%d] has empty ID", i)
			continue
		}
		id, err := strconv.ParseUint(ev.ID, 10, 64)
		if err != nil {
			t.Errorf("events[%d].ID %q is not a valid uint64: %v", i, ev.ID, err)
			continue
		}
		if prevID != 0 && id <= prevID {
			t.Errorf("events[%d].ID %d is not greater than previous %d", i, id, prevID)
		}
		prevID = id
	}
}

func TestSSEIntegration_LastEventID_Replay(t *testing.T) {
	broadcaster := sse.NewBroadcaster()

	// First router/run to generate initial events.
	router1, manager1 := buildSSERouter(t, "sse-replay-pol1", sseOneTurnMsgs(), broadcaster)
	srv := httptest.NewServer(router1)
	t.Cleanup(srv.Close)

	resp1 := connectSSE(t, srv.URL, "")
	fireWebhook(t, router1, "sse-replay-pol1")
	firstEvents := drainSSEEvents(t, resp1, 4, 10*time.Second)
	resp1.Body.Close()
	manager1.Wait()

	if len(firstEvents) == 0 {
		t.Fatal("no events received in first connection")
	}
	lastSeenID := firstEvents[len(firstEvents)-1].ID

	// Fire a second run (against a fresh store) while the client is
	// disconnected. Both share the broadcaster so events land in the ring.
	router2, manager2 := buildSSERouter(t, "sse-replay-pol2", sseOneTurnMsgs(), broadcaster)
	// We need an httptest server for fireWebhook to POST against, but we can
	// call fireWebhook with router2 directly (no real TCP needed).
	fireWebhook(t, router2, "sse-replay-pol2")
	manager2.Wait()

	// Reconnect with Last-Event-ID set to the last ID observed in the first batch.
	resp2 := connectSSE(t, srv.URL, lastSeenID)
	t.Cleanup(func() { resp2.Body.Close() })

	replayedEvents := drainSSEEvents(t, resp2, 2, 10*time.Second)
	if len(replayedEvents) == 0 {
		t.Fatal("no replayed events received after Last-Event-ID reconnect")
	}

	// All replayed events must have IDs strictly greater than lastSeenID.
	lastSeenNum, err := strconv.ParseUint(lastSeenID, 10, 64)
	if err != nil {
		t.Fatalf("lastSeenID %q is not a valid uint64: %v", lastSeenID, err)
	}
	firstIDs := make(map[string]bool, len(firstEvents))
	for _, ev := range firstEvents {
		firstIDs[ev.ID] = true
	}
	for i, ev := range replayedEvents {
		evNum, err := strconv.ParseUint(ev.ID, 10, 64)
		if err != nil {
			t.Errorf("replayed event[%d].ID %q is not a valid uint64: %v", i, ev.ID, err)
			continue
		}
		if evNum <= lastSeenNum {
			t.Errorf("replayed event[%d].ID %d is not > lastSeenID %d", i, evNum, lastSeenNum)
		}
		if firstIDs[ev.ID] {
			t.Errorf("replayed event[%d].ID %q was already seen in first connection (duplicate)", i, ev.ID)
		}
	}
}

func TestSSEIntegration_SlowClientIsolation(t *testing.T) {
	// Small channel size so the slow client's buffer fills almost immediately.
	broadcaster := sse.NewBroadcaster(sse.WithChannelSize(2))
	router, manager := buildSSERouter(t, "sse-slow-pol", sseOneTurnMsgs(), broadcaster)

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	// Fast client actively reads events.
	fastResp := connectSSE(t, srv.URL, "")
	t.Cleanup(func() { fastResp.Body.Close() })

	// Slow client connects but never reads — its channel fills and events are
	// dropped for it rather than blocking delivery to the fast client.
	slowResp, err := http.Get(srv.URL + "/api/v1/events") //nolint:noctx
	if err != nil {
		t.Fatalf("slow client connect: %v", err)
	}
	t.Cleanup(func() { slowResp.Body.Close() })

	fireWebhook(t, router, "sse-slow-pol")

	fastEvents := drainSSEEvents(t, fastResp, 2, 10*time.Second)
	manager.Wait()

	if len(fastEvents) < 2 {
		t.Errorf("fast client received only %d events, want >= 2; events: %v", len(fastEvents), fastEvents)
	}
	for i, ev := range fastEvents {
		if ev.Type == "" {
			t.Errorf("fastEvents[%d].Type is empty", i)
		}
		if ev.ID == "" {
			t.Errorf("fastEvents[%d].ID is empty", i)
		}
	}
}
