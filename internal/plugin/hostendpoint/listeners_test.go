package hostendpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/mcp"
)

// discoverOver completes a server/discover round-trip against addr and
// returns the HTTP status. Errors are returned, not fataled, because half
// the tests assert the connection FAILING.
func discoverOver(addr string) (int, error) {
	raw, err := json.Marshal(discoverBody(mcp.ProtocolVersion20260728))
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/", bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("MCP-Protocol-Version", mcp.ProtocolVersion20260728)
	req.Header.Set("Mcp-Method", "server/discover")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func TestListenerSet(t *testing.T) {
	t.Run("serves discover on the bound address and nowhere else", func(t *testing.T) {
		ls := NewListenerSet(NewServer())
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ls.Close(ctx) //nolint:errcheck
		})

		// 127.0.0.1 stands in for a per-instance network's gateway address:
		// a specific interface address, not a wildcard. Linux serves the
		// whole 127/8 block, so 127.0.0.2 below is a genuinely different
		// address the listener was NOT bound to — the closest a unit test
		// gets to "a process off the instance network cannot reach it"
		// (the real-daemon version belongs to the substrate suite).
		bound, err := ls.Add("inst-a", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("Add: %v", err)
		}

		status, err := discoverOver(bound)
		if err != nil {
			t.Fatalf("discover over %s: %v", bound, err)
		}
		if status != http.StatusOK {
			t.Fatalf("discover status = %d, want 200", status)
		}

		_, port, err := net.SplitHostPort(bound)
		if err != nil {
			t.Fatalf("split bound addr: %v", err)
		}
		if _, err := discoverOver("127.0.0.2:" + port); err == nil {
			t.Fatal("discover succeeded on an address the listener is not bound to — the endpoint is not per-network")
		}
	})

	t.Run("refuses wildcard bind addresses", func(t *testing.T) {
		ls := NewListenerSet(NewServer())
		for _, addr := range []string{"0.0.0.0:0", "[::]:0", ":0"} {
			if _, err := ls.Add("inst-w", addr); !errors.Is(err, ErrWildcardAddr) {
				t.Errorf("Add(%q) error = %v, want ErrWildcardAddr", addr, err)
			}
		}
	})

	t.Run("a second Add for a live instance is an error", func(t *testing.T) {
		ls := NewListenerSet(NewServer())
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ls.Close(ctx) //nolint:errcheck
		})
		if _, err := ls.Add("inst-b", "127.0.0.1:0"); err != nil {
			t.Fatalf("first Add: %v", err)
		}
		if _, err := ls.Add("inst-b", "127.0.0.1:0"); err == nil || !strings.Contains(err.Error(), "already has a listener") {
			t.Fatalf("second Add error = %v, want already-has-a-listener", err)
		}
	})

	t.Run("Remove stops serving and is idempotent", func(t *testing.T) {
		ls := NewListenerSet(NewServer())
		bound, err := ls.Add("inst-c", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("Add: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := ls.Remove(ctx, "inst-c"); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if _, err := discoverOver(bound); err == nil {
			t.Fatal("discover succeeded after Remove")
		}
		if got := ls.Addr("inst-c"); got != "" {
			t.Errorf("Addr after Remove = %q, want empty", got)
		}
		// Level-triggered caller contract: removing what is already gone is
		// a converged state, not an error.
		if err := ls.Remove(ctx, "inst-c"); err != nil {
			t.Fatalf("second Remove: %v", err)
		}
	})

	t.Run("the freed address is bindable again after Remove", func(t *testing.T) {
		// Pins that Remove releases the socket, not just the map entry — the
		// reconciler re-creates instance networks with the same gateway
		// address, so a leaked listener would wedge every re-create.
		ls := NewListenerSet(NewServer())
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ls.Close(ctx) //nolint:errcheck
		})
		bound, err := ls.Add("inst-d", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := ls.Remove(ctx, "inst-d"); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		rebound, err := ls.Add("inst-d", bound)
		if err != nil {
			t.Fatalf("re-Add on the freed address: %v", err)
		}
		if status, err := discoverOver(rebound); err != nil || status != http.StatusOK {
			t.Fatalf("discover after re-Add: status=%d err=%v", status, err)
		}
	})
}
