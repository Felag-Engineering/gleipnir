package netguard

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"
)

// TestAccept_RealSocketDenyByLocalAddr is the acceptance test the loopback
// alias gives us for free: a wildcard-bound listener's LocalAddr reflects
// whichever local address a client actually dialed, so this exercises the
// exact real-world shape the guard defends — a wildcard operator-API listener
// reachable on more than one local address, some inside the pool, some not —
// without needing a real plugin network.
//
// 127.0.0.2 is not guaranteed to be brought up on every platform/sandbox
// (only 127.0.0.1 is universally guaranteed), so this skips rather than
// fails where it isn't dialable.
func TestAccept_RealSocketDenyByLocalAddr(t *testing.T) {
	probeLn, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("127.0.0.2 is not dialable on this platform/sandbox: %v", err)
	}
	probeLn.Close()

	rawLn, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	pool := netip.MustParsePrefix("127.0.0.2/32")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	guarded, err := Wrap(rawLn, pool, logger)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	t.Cleanup(func() { guarded.Close() })

	port := guarded.Addr().(*net.TCPAddr).Port

	type acceptOutcome struct {
		conn net.Conn
		err  error
	}
	accepted := make(chan acceptOutcome, 2)
	go func() {
		for {
			conn, err := guarded.Accept()
			accepted <- acceptOutcome{conn: conn, err: err}
			if err != nil {
				return
			}
		}
	}()

	// Dial the denied alias: the guard refuses it (with SetLinger(0), an
	// immediate RST) before any bytes flow. Depending on how fast the RST
	// lands relative to the client's own connect() call, that can surface
	// either as the dial itself failing or as a successful dial whose first
	// read fails — both are the refusal, so this accepts either.
	denied, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.2:%d", port), 5*time.Second)
	if err == nil {
		defer denied.Close()
		if err := denied.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("SetReadDeadline: %v", err)
		}
		if _, readErr := denied.Read(make([]byte, 1)); readErr == nil {
			t.Fatal("expected the denied dial to be closed by the guard, got a successful read")
		}
	}

	// Dial the allowed address: it must reach Accept and be handed back live.
	allowed, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
	if err != nil {
		t.Fatalf("dial 127.0.0.1: %v", err)
	}
	defer allowed.Close()

	select {
	case outcome := <-accepted:
		if outcome.err != nil {
			t.Fatalf("Accept returned an error instead of the allowed conn: %v", outcome.err)
		}
		outcome.conn.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the allowed connection to be accepted")
	}

	if refused := guarded.Refused(); refused != 1 {
		t.Errorf("Refused() = %d, want 1", refused)
	}
}
