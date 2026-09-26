package netguard

import (
	"bytes"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeConn is a minimal net.Conn double. It embeds a nil net.Conn and only
// overrides the three methods Accept actually calls (LocalAddr, RemoteAddr,
// Close) — any other method would panic, which is intentional: a test that
// exercises more than that means the guard started depending on something it
// shouldn't.
type fakeConn struct {
	net.Conn
	local  net.Addr
	remote net.Addr

	mu     sync.Mutex
	closed bool
}

// defaultOutsideRemote is the RemoteAddr newFakeConn uses when a test only
// cares about LocalAddr — a real, public, pool-irrelevant address (TEST-NET-2,
// RFC 5737) so the RemoteAddr-in-pool check never accidentally fires for
// tests that aren't exercising it.
var defaultOutsideRemote = &net.TCPAddr{IP: net.ParseIP("198.51.100.1"), Port: 55555}

func newFakeConn(local net.Addr) *fakeConn {
	return &fakeConn{local: local, remote: defaultOutsideRemote}
}

func newFakeConnWithRemote(local, remote net.Addr) *fakeConn {
	return &fakeConn{local: local, remote: remote}
}

func (c *fakeConn) LocalAddr() net.Addr  { return c.local }
func (c *fakeConn) RemoteAddr() net.Addr { return c.remote }

func (c *fakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *fakeConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// stringAddr is a net.Addr whose String() is not a valid host:port pair, used
// to exercise the "can't determine local IP" fallback.
type stringAddr string

func (a stringAddr) Network() string { return "test" }
func (a stringAddr) String() string  { return string(a) }

func tcpAddr(t *testing.T, ip string, port int) *net.TCPAddr {
	t.Helper()
	parsed := net.ParseIP(ip)
	if parsed == nil {
		t.Fatalf("test setup: %q is not a valid IP", ip)
	}
	return &net.TCPAddr{IP: parsed, Port: port}
}

// acceptResult is one queued (conn, err) pair a fakeListener hands back from
// Accept, in order.
type acceptResult struct {
	conn net.Conn
	err  error
}

// fakeListener is a net.Listener test double whose Accept plays back a fixed
// script of results, so a test controls exactly which LocalAddr each
// simulated connection carries — no real network aliasing needed.
type fakeListener struct {
	mu         sync.Mutex
	results    []acceptResult
	closeCalls int
	closeErr   error
}

func (l *fakeListener) enqueue(conn net.Conn, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.results = append(l.results, acceptResult{conn: conn, err: err})
}

func (l *fakeListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.results) == 0 {
		// A test that reaches this has made Accept loop more times than it
		// scripted for — exactly the "must never spin" failure this test
		// double is built to catch loudly instead of blocking forever.
		return nil, errors.New("fakeListener: Accept called with no result queued")
	}
	r := l.results[0]
	l.results = l.results[1:]
	return r.conn, r.err
}

func (l *fakeListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closeCalls++
	return l.closeErr
}

func (l *fakeListener) Addr() net.Addr { return &net.TCPAddr{} }

func testPool(t *testing.T) netip.Prefix {
	t.Helper()
	return netip.MustParsePrefix("10.83.0.0/16")
}

func TestWrap_RejectsInvalidInputs(t *testing.T) {
	t.Run("nil listener", func(t *testing.T) {
		if _, err := Wrap(nil, testPool(t), nil); err == nil {
			t.Fatal("expected error for nil listener, got nil")
		}
	})

	t.Run("invalid pool", func(t *testing.T) {
		if _, err := Wrap(&fakeListener{}, netip.Prefix{}, nil); err == nil {
			t.Fatal("expected error for invalid pool, got nil")
		}
	})

	t.Run("nil logger falls back to default", func(t *testing.T) {
		ln, err := Wrap(&fakeListener{}, testPool(t), nil)
		if err != nil {
			t.Fatalf("Wrap: %v", err)
		}
		if ln.logger == nil {
			t.Fatal("expected a default logger, got nil")
		}
	})
}

func TestInstalled(t *testing.T) {
	fl := &fakeListener{}
	guarded, err := Wrap(fl, testPool(t), nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if !Installed(guarded) {
		t.Error("Installed(guarded) = false, want true")
	}
	if Installed(fl) {
		t.Error("Installed(unwrapped) = true, want false")
	}
}

func TestAccept_RefusesConnInsidePool(t *testing.T) {
	fl := &fakeListener{}
	inside := newFakeConn(tcpAddr(t, "10.83.5.1", 12345))
	outside := newFakeConn(tcpAddr(t, "192.168.1.5", 12345))
	fl.enqueue(inside, nil)
	fl.enqueue(outside, nil)

	ln, err := Wrap(fl, testPool(t), nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	got, err := ln.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if got != outside {
		t.Errorf("Accept returned %v, want the outside-pool conn", got)
	}
	if !inside.isClosed() {
		t.Error("the in-pool conn was not closed")
	}
	if outside.isClosed() {
		t.Error("the out-of-pool conn was closed, but should have been returned live")
	}
	if refused := ln.Refused(); refused != 1 {
		t.Errorf("Refused() = %d, want 1", refused)
	}
}

func TestAccept_AcceptsConnOutsidePool(t *testing.T) {
	fl := &fakeListener{}
	outside := newFakeConn(tcpAddr(t, "203.0.113.9", 443))
	fl.enqueue(outside, nil)

	ln, err := Wrap(fl, testPool(t), nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	got, err := ln.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if got != outside {
		t.Errorf("Accept returned %v, want %v", got, outside)
	}
	if outside.isClosed() {
		t.Error("conn outside the pool must not be closed")
	}
	if refused := ln.Refused(); refused != 0 {
		t.Errorf("Refused() = %d, want 0", refused)
	}
}

// TestAccept_HTTPHeadersNeverConsulted documents (rather than merely asserts)
// the property the issue calls out: forged X-Forwarded-For/X-Real-IP headers
// cannot affect this decision because it happens before any byte of the HTTP
// request has been read — Accept works from net.Conn.LocalAddr/RemoteAddr
// alone, and no *http.Request exists yet at this layer for a header to live
// on.
func TestAccept_HTTPHeadersNeverConsulted(t *testing.T) {
	fl := &fakeListener{}
	outside := newFakeConn(tcpAddr(t, "203.0.113.9", 443))
	fl.enqueue(outside, nil)

	ln, err := Wrap(fl, testPool(t), nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if _, err := ln.Accept(); err != nil {
		t.Fatalf("Accept: %v", err)
	}
}

func TestAccept_RefusesConnWhenRemoteAddrInPool(t *testing.T) {
	fl := &fakeListener{}
	// LocalAddr looks fine on its own — this is the weak-host-model case: a
	// plugin container reaching Gleipnir's compose-network address through
	// its own interface, arriving with a LocalAddr the primary check would
	// pass, but a RemoteAddr that gives away where it actually came from.
	conn := newFakeConnWithRemote(
		tcpAddr(t, "203.0.113.9", 443),
		tcpAddr(t, "10.83.0.7", 51234),
	)
	fl.enqueue(conn, nil)

	ln, err := Wrap(fl, testPool(t), nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	fl.enqueue(newFakeConn(tcpAddr(t, "203.0.113.9", 444)), nil)
	got, err := ln.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if got == conn {
		t.Error("Accept returned the conn whose RemoteAddr is inside the pool")
	}
	if !conn.isClosed() {
		t.Error("conn with an in-pool RemoteAddr was not closed")
	}
	if refused := ln.Refused(); refused != 1 {
		t.Errorf("Refused() = %d, want 1", refused)
	}
}

// TestAccept_OutOfPoolRemoteAddrNeverRescuesAnInPoolLocalAddr pins the
// asymmetry the package doc describes: RemoteAddr can only add a refusal, it
// can never withdraw one the LocalAddr check already made.
func TestAccept_OutOfPoolRemoteAddrNeverRescuesAnInPoolLocalAddr(t *testing.T) {
	fl := &fakeListener{}
	conn := newFakeConnWithRemote(
		tcpAddr(t, "10.83.5.1", 12345), // in the pool
		tcpAddr(t, "203.0.113.9", 443), // nowhere near it
	)
	fl.enqueue(conn, nil)
	fl.enqueue(newFakeConn(tcpAddr(t, "203.0.113.9", 444)), nil)

	ln, err := Wrap(fl, testPool(t), nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	if _, err := ln.Accept(); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if !conn.isClosed() {
		t.Error("an in-pool LocalAddr must be refused regardless of RemoteAddr")
	}
}

func TestAccept_RefusesLinkLocalLocalAddr(t *testing.T) {
	fl := &fakeListener{}
	conn := newFakeConn(&net.TCPAddr{IP: net.ParseIP("fe80::1"), Port: 8080})
	fl.enqueue(conn, nil)
	fl.enqueue(newFakeConn(tcpAddr(t, "203.0.113.9", 444)), nil)

	ln, err := Wrap(fl, testPool(t), nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	if _, err := ln.Accept(); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if !conn.isClosed() {
		t.Error("a link-local (fe80::/10) LocalAddr must be refused")
	}
}

func TestAccept_RefusesLinkLocalRemoteAddr(t *testing.T) {
	fl := &fakeListener{}
	conn := newFakeConnWithRemote(
		tcpAddr(t, "203.0.113.9", 443),
		&net.TCPAddr{IP: net.ParseIP("fe80::1"), Port: 8080},
	)
	fl.enqueue(conn, nil)
	fl.enqueue(newFakeConn(tcpAddr(t, "203.0.113.9", 444)), nil)

	ln, err := Wrap(fl, testPool(t), nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	if _, err := ln.Accept(); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if !conn.isClosed() {
		t.Error("a link-local (fe80::/10) RemoteAddr must be refused")
	}
}

func TestAccept_NeverSpinsOnRunOfRefusals(t *testing.T) {
	fl := &fakeListener{}
	const refusals = 50
	insideConns := make([]*fakeConn, refusals)
	for i := range insideConns {
		insideConns[i] = newFakeConn(tcpAddr(t, "10.83.0.1", 9000+i))
		fl.enqueue(insideConns[i], nil)
	}
	outside := newFakeConn(tcpAddr(t, "198.51.100.7", 8080))
	fl.enqueue(outside, nil)

	ln, err := Wrap(fl, testPool(t), nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	got, err := ln.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if got != outside {
		t.Errorf("Accept returned %v, want the outside-pool conn", got)
	}
	for i, c := range insideConns {
		if !c.isClosed() {
			t.Errorf("in-pool conn %d was not closed", i)
		}
	}
	if refused := ln.Refused(); refused != refusals {
		t.Errorf("Refused() = %d, want %d", refused, refusals)
	}
}

func TestAccept_PropagatesUnderlyingErrorsVerbatim(t *testing.T) {
	t.Run("permanent close error", func(t *testing.T) {
		fl := &fakeListener{}
		fl.enqueue(nil, net.ErrClosed)

		ln, err := Wrap(fl, testPool(t), nil)
		if err != nil {
			t.Fatalf("Wrap: %v", err)
		}

		_, acceptErr := ln.Accept()
		if !errors.Is(acceptErr, net.ErrClosed) {
			t.Errorf("Accept error = %v, want net.ErrClosed", acceptErr)
		}
	})

	t.Run("arbitrary transient error", func(t *testing.T) {
		fl := &fakeListener{}
		wantErr := errors.New("transient accept failure")
		fl.enqueue(nil, wantErr)

		ln, err := Wrap(fl, testPool(t), nil)
		if err != nil {
			t.Fatalf("Wrap: %v", err)
		}

		_, acceptErr := ln.Accept()
		if !errors.Is(acceptErr, wantErr) {
			t.Errorf("Accept error = %v, want %v", acceptErr, wantErr)
		}
	})
}

// TestAccept_FailsClosedWhenLocalAddrUnparseable pins B1: a LocalAddr the
// guard cannot understand is exactly the case it cannot prove safe, so it is
// refused rather than let through. This should never happen against a real
// *net.TCPAddr (see TestAccept_RealSocketDenyByLocalAddr for that path); it
// only matters for whatever future net.Conn implementation might produce
// something this package's address parsing can't handle.
func TestAccept_FailsClosedWhenLocalAddrUnparseable(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	fl := &fakeListener{}
	conn := newFakeConn(stringAddr("not-a-host-port"))
	fl.enqueue(conn, nil)
	fl.enqueue(newFakeConn(tcpAddr(t, "203.0.113.9", 444)), nil)

	ln, err := Wrap(fl, testPool(t), logger)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	if _, err := ln.Accept(); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if !conn.isClosed() {
		t.Error("a conn whose local address can't be parsed must be refused, not accepted")
	}
	if refused := ln.Refused(); refused != 1 {
		t.Errorf("Refused() = %d, want 1", refused)
	}
	if !strings.Contains(buf.String(), "reason=local_addr_unparseable") {
		t.Errorf("expected a distinct reason for the unparseable-LocalAddr refusal; log:\n%s", buf.String())
	}
}

func TestClose_PropagatesToUnderlyingListener(t *testing.T) {
	fl := &fakeListener{}
	ln, err := Wrap(fl, testPool(t), nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	// Close is not overridden by Listener — it is promoted straight through
	// to the embedded net.Listener, which is what lets http.Server.Shutdown
	// unblock a wrapped listener's Accept the same way it would an
	// unwrapped one.
	if err := ln.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fl.closeCalls != 1 {
		t.Errorf("underlying Close called %d times, want 1", fl.closeCalls)
	}
}

// TestAccept_RefusalLoggingIsRateLimited exercises the same coalescing shape
// as internal/plugin/logcapture's countDrop: the first refusal in a window
// logs immediately (so an operator sees a leak right away), everything else
// in that window accumulates silently, and the next log line — once the
// interval has elapsed — reports the accumulated count so nothing is lost to
// coalescing, just delayed.
func TestAccept_RefusalLoggingIsRateLimited(t *testing.T) {
	fakeNow := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	origTimeNow := timeNow
	timeNow = func() time.Time { return fakeNow }
	t.Cleanup(func() { timeNow = origTimeNow })

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	fl := &fakeListener{}
	const burst = 5
	for i := 0; i < burst; i++ {
		fl.enqueue(newFakeConn(tcpAddr(t, "10.83.0.9", 9000+i)), nil)
	}
	fl.enqueue(newFakeConn(tcpAddr(t, "198.51.100.7", 8080)), nil)

	ln, err := Wrap(fl, testPool(t), logger)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if _, err := ln.Accept(); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	// The first refusal in the burst logs immediately; the other four in the
	// same window are coalesced into it rather than each printing their own
	// line.
	lines := strings.Count(buf.String(), "netguard: refused")
	if lines != 1 {
		t.Fatalf("log lines after first burst = %d, want 1 (coalesced); log:\n%s", lines, buf.String())
	}
	if !strings.Contains(buf.String(), "count=1") {
		t.Errorf("expected the first refusal to log immediately with count=1; log:\n%s", buf.String())
	}

	// Still within the log interval: more refusals accumulate but do not log.
	fl.enqueue(newFakeConn(tcpAddr(t, "10.83.0.9", 9100)), nil)
	fl.enqueue(newFakeConn(tcpAddr(t, "198.51.100.7", 8081)), nil)
	if _, err := ln.Accept(); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if lines := strings.Count(buf.String(), "netguard: refused"); lines != 1 {
		t.Fatalf("log lines within the interval = %d, want still 1; log:\n%s", lines, buf.String())
	}

	// Past the interval, the next refusal reports the count accumulated since
	// the first line: 4 unlogged from the initial burst of 5 + 1 from the
	// still-within-interval refusal above + this one = 6.
	fakeNow = fakeNow.Add(refusalLogInterval + time.Second)
	fl.enqueue(newFakeConn(tcpAddr(t, "10.83.0.9", 9200)), nil)
	fl.enqueue(newFakeConn(tcpAddr(t, "198.51.100.7", 8082)), nil)
	if _, err := ln.Accept(); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if lines := strings.Count(buf.String(), "netguard: refused"); lines != 2 {
		t.Fatalf("log lines after the interval elapsed = %d, want 2; log:\n%s", lines, buf.String())
	}
	if !strings.Contains(buf.String(), "count=6") {
		t.Errorf("expected the second log line to report the coalesced count; log:\n%s", buf.String())
	}

	if refused := ln.Refused(); refused != burst+2 {
		t.Errorf("Refused() = %d, want %d (lifetime total, independent of log coalescing)", refused, burst+2)
	}
}

// TestAddrIP_NilSafety pins finding 3: neither a nil net.Addr nor a
// typed-nil *net.TCPAddr (an interface holding a nil pointer — distinct from
// a nil interface itself) may panic. Both must fail closed: (netip.Addr{},
// false), which classify then turns into reasonLocalUnparseable /
// "no additional RemoteAddr check", not a crash that would take down the
// whole Accept loop.
func TestAddrIP_NilSafety(t *testing.T) {
	t.Run("nil net.Addr interface", func(t *testing.T) {
		ip, ok := addrIP(nil)
		if ok {
			t.Errorf("addrIP(nil) = (%v, true), want ok=false", ip)
		}
	})

	t.Run("typed-nil *net.TCPAddr", func(t *testing.T) {
		var typedNil *net.TCPAddr
		var addr net.Addr = typedNil // non-nil interface, nil underlying pointer
		ip, ok := addrIP(addr)
		if ok {
			t.Errorf("addrIP(typed-nil *net.TCPAddr) = (%v, true), want ok=false", ip)
		}
	})
}

// TestFormatAddr_NilSafety pins the other half of finding 3: the log-line
// formatter must not panic on the same two nil shapes addrIP guards against,
// since a refusal caused by one of them logs that same unparseable address.
func TestFormatAddr_NilSafety(t *testing.T) {
	t.Run("nil net.Addr interface", func(t *testing.T) {
		if got := formatAddr(nil); got != "<nil>" {
			t.Errorf("formatAddr(nil) = %q, want %q", got, "<nil>")
		}
	})

	t.Run("typed-nil *net.TCPAddr", func(t *testing.T) {
		var typedNil *net.TCPAddr
		var addr net.Addr = typedNil
		if got := formatAddr(addr); got != "<nil>" {
			t.Errorf("formatAddr(typed-nil) = %q, want %q", got, "<nil>")
		}
	})

	t.Run("ordinary address is unaffected", func(t *testing.T) {
		addr := &net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 443}
		if got := formatAddr(addr); got != addr.String() {
			t.Errorf("formatAddr(addr) = %q, want %q", got, addr.String())
		}
	})
}

// TestAccept_NilLocalAddrDoesNotPanic exercises the nil-safety fix at the
// level that actually matters: a conn whose LocalAddr is nil must be refused
// (and logged) like any other unparseable-address conn, not crash Accept.
func TestAccept_NilLocalAddrDoesNotPanic(t *testing.T) {
	fl := &fakeListener{}
	conn := newFakeConn(nil)
	fl.enqueue(conn, nil)
	fl.enqueue(newFakeConn(tcpAddr(t, "203.0.113.9", 444)), nil)

	ln, err := Wrap(fl, testPool(t), nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	if _, err := ln.Accept(); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if !conn.isClosed() {
		t.Error("a conn with a nil LocalAddr must be refused, not accepted")
	}
}

// TestAccept_NilRemoteAddrDoesNotPanic covers the RemoteAddr side: even when
// LocalAddr passes, a nil RemoteAddr must not panic the RemoteAddr-in-pool
// check or the log line, and the connection is accepted since a nil
// RemoteAddr carries no refusal signal.
func TestAccept_NilRemoteAddrDoesNotPanic(t *testing.T) {
	fl := &fakeListener{}
	conn := newFakeConnWithRemote(tcpAddr(t, "203.0.113.9", 443), nil)
	fl.enqueue(conn, nil)

	ln, err := Wrap(fl, testPool(t), nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	got, err := ln.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if got != conn {
		t.Error("a conn with a nil RemoteAddr but a fine LocalAddr must be accepted")
	}
}

// TestWrap_WithOnRefuse pins finding 4: the refusal callback is how a caller
// (main.go) hooks metrics into the guard without this package importing a
// metrics registry itself. It must fire with the right reason string exactly
// once per refusal, and never fire for an accepted connection.
func TestWrap_WithOnRefuse(t *testing.T) {
	fl := &fakeListener{}
	refused := newFakeConn(tcpAddr(t, "10.83.5.1", 12345))
	accepted := newFakeConn(tcpAddr(t, "203.0.113.9", 443))
	fl.enqueue(refused, nil)
	fl.enqueue(accepted, nil)

	var mu sync.Mutex
	var gotReasons []string
	ln, err := Wrap(fl, testPool(t), nil, WithOnRefuse(func(reason string) {
		mu.Lock()
		defer mu.Unlock()
		gotReasons = append(gotReasons, reason)
	}))
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	got, err := ln.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if got != accepted {
		t.Errorf("Accept returned %v, want the accepted conn", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotReasons) != 1 || gotReasons[0] != string(reasonLocalInPool) {
		t.Errorf("OnRefuse callbacks = %v, want exactly [%q]", gotReasons, reasonLocalInPool)
	}
}

// TestWrap_WithoutOnRefuseIsSafe confirms Wrap works with no callback at all
// (the default) — recordRefusal must not require one.
func TestWrap_WithoutOnRefuseIsSafe(t *testing.T) {
	fl := &fakeListener{}
	fl.enqueue(newFakeConn(tcpAddr(t, "10.83.5.1", 12345)), nil)
	fl.enqueue(newFakeConn(tcpAddr(t, "203.0.113.9", 443)), nil)

	ln, err := Wrap(fl, testPool(t), nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if _, err := ln.Accept(); err != nil {
		t.Fatalf("Accept: %v", err)
	}
}

// TestRefusalMessage_RemoteInPoolMentionsOperatorCase pins the exact
// operator-facing wording finding 1 requires for the remote_addr_in_pool
// reason — an operator scanning logs needs to immediately understand that a
// refused client might be their own, not a plugin.
func TestRefusalMessage_RemoteInPoolMentionsOperatorCase(t *testing.T) {
	remote := tcpAddr(t, "10.83.0.7", 51234)
	msg := refusalMessage(reasonRemoteInPool, tcpAddr(t, "203.0.113.9", 443), remote)
	want := "client source 10.83.0.7:51234 is inside GLEIPNIR_PLUGIN_SUBNET_POOL; if this is a legitimate operator, choose a non-overlapping pool"
	if msg != want {
		t.Errorf("refusalMessage(reasonRemoteInPool, ...) = %q, want %q", msg, want)
	}
}
