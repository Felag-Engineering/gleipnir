package sse

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Handler serves the GET /api/v1/events SSE endpoint.
// It subscribes new clients, replays buffered events for reconnecting clients
// (via Last-Event-ID), and streams live events until the client disconnects.
type Handler struct {
	broadcaster EventBroadcaster
}

// NewHandler returns an SSE Handler backed by the given broadcaster.
func NewHandler(b EventBroadcaster) *Handler {
	return &Handler{broadcaster: b}
}

// ServeHTTP implements http.Handler. It:
//  1. Sets the required SSE response headers (including X-Accel-Buffering: no
//     to disable nginx proxy buffering).
//  2. Disables the server's WriteTimeout so long-lived connections are not
//     killed mid-stream.
//  3. Subscribes to the broadcaster before replaying buffered events, so no
//     events published between replay and the live loop are missed.
//  4. Sends a heartbeat comment every 15 s to keep the TCP connection alive
//     through proxies and load balancers that close idle streams.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Disable the server WriteTimeout for this connection. Without this, the
	// server would close the SSE stream after WriteTimeout (typically 15 s).
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	// Subscribe first so we don't miss events published during replay.
	sub := h.broadcaster.Subscribe()
	defer h.broadcaster.Unsubscribe(sub)

	// Replay buffered events for a reconnecting client.
	var lastSentID uint64
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		if id, err := strconv.ParseUint(raw, 10, 64); err == nil {
			lastSentID = id
		}
	}
	for _, ev := range h.broadcaster.Replay(lastSentID) {
		writeEvent(w, ev)
		lastSentID = ev.ID
	}
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				return
			}
			// Deduplicate in case a replayed event also arrived on the channel.
			if ev.ID <= lastSentID {
				continue
			}
			writeEvent(w, ev)
			lastSentID = ev.ID
			flusher.Flush()

		case <-heartbeat.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()

		case <-r.Context().Done():
			return
		}
	}
}

func writeEvent(w http.ResponseWriter, ev Event) {
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, ev.Data)
}
