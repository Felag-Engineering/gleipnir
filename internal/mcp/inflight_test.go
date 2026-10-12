package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// InFlight must count across every Client built for a server, because
// ResolveForPolicy builds a fresh Client per resolve and the admin gate has to
// see all of them.
func TestRegistryInFlight_CountsAcrossClientsAndDrops(t *testing.T) {
	entered := make(chan struct{}, 4)
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-unblock
		http.Error(w, "done", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	reg := NewRegistry(nil)
	row := db.McpServer{ID: "srv-1", Name: "svc", Url: srv.URL}

	if got := reg.InFlight("srv-1"); got != 0 {
		t.Fatalf("InFlight before any call = %d, want 0", got)
	}

	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		client := reg.newClientForServer(row)
		go func() {
			_, _ = client.CallTool(context.Background(), "any", nil, CallOptions{})
			done <- struct{}{}
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(30 * time.Second):
			t.Fatal("call never reached the server")
		}
	}

	if got := reg.InFlight("srv-1"); got != 2 {
		t.Errorf("InFlight with two calls on separate clients = %d, want 2", got)
	}
	if got := reg.InFlight("srv-other"); got != 0 {
		t.Errorf("InFlight for another server = %d, want 0", got)
	}

	close(unblock)
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("call never returned")
		}
	}
	if got := reg.InFlight("srv-1"); got != 0 {
		t.Errorf("InFlight after calls returned = %d, want 0", got)
	}
}
