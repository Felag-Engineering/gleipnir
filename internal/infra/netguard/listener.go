// Package netguard wraps a net.Listener so it refuses connections that
// arrive on, or come from, an address inside a denied subnet.
//
// It is a strict leaf package — no internal imports — so main.go can wire it
// around the operator API's listener without pulling in the plugin substrate
// or Gleipnir's metrics registry. A caller that wants metrics (main.go does)
// hooks WithOnRefuse into Wrap instead of this package reaching out to
// internal/infra/metrics itself.
//
// # Why LocalAddr is the primary signal
//
// The main check reads net.Conn.LocalAddr(): the kernel-assigned address the
// connection actually arrived on. Unlike RemoteAddr or an HTTP
// X-Forwarded-For/X-Real-IP header, LocalAddr is much harder for a peer to
// influence — it is set by the receiving kernel's own routing decision, not
// carried in anything the peer sends. The decision here also happens before
// any HTTP request is even parsed, so those headers never enter into it at
// all, forged or not.
//
// # Why RemoteAddr denies but never allows
//
// Accept also refuses a connection whose RemoteAddr falls inside the pool,
// as a second layer against the "weak host model" case: a plugin container
// that can route to Gleipnir's compose-network address through its own
// interface, arriving with a LocalAddr that is NOT the plugin gateway
// address the primary check expects. RemoteAddr is asymmetric on purpose —
// it can only turn an otherwise-accepted connection into a refusal, never
// the reverse. That asymmetry is safe because completing a TCP handshake
// with a *forged* source address is not the same class of problem as
// forging an HTTP header: the connecting side never sees the SYN-ACK sent to
// a spoofed address, so it cannot complete the handshake and reach the point
// where Accept() runs at all — a peer can lie about its RemoteAddr, but only
// by giving up the ability to talk back on that same connection. So a
// RemoteAddr genuinely inside the pool is real signal (this really is a
// pool-address peer) and safe to refuse on. An out-of-pool RemoteAddr is not
// treated as proof of legitimacy in the other direction — it is simply not
// used as a reason to override whatever the LocalAddr check already decided.
//
// This does mean a legitimate operator whose own client machine happens to
// sit in an address range that overlaps GLEIPNIR_PLUGIN_SUBNET_POOL will be
// refused too — the guard cannot distinguish "an operator dialing in from an
// address that happens to overlap the pool" from "a plugin instance". The
// remote_addr_in_pool refusal log line says so explicitly, because the fix
// in that case is operator-side (choose a pool that doesn't overlap real
// client address space), not a bypass in this package.
//
// # Link-local addresses
//
// Both LocalAddr and RemoteAddr are also checked against fe80::/10
// (IPv6 link-local unicast) independent of the deny pool: operator API
// traffic never legitimately arrives over a link-local address, and a
// dual-stack host can expose one on the same interfaces the pool's IPv4
// addresses live on.
//
// # No escape hatch for the startup overlap check
//
// CheckPoolOverlap (see overlap.go) refuses to start the server at all if
// GLEIPNIR_PLUGIN_SUBNET_POOL overlaps a real interface's subnet on the host
// (not just a single address on it — see CheckPoolOverlap's doc). There is
// deliberately no override flag for that check: a false negative there —
// accepting an overlapping pool — would silently defeat the whole guard by
// making a legitimate host address look like a plugin-network address. Once
// the container substrate self-attaches Gleipnir to every per-instance
// network (#958, wired by the future #962 assembly), Gleipnir's own
// interfaces will legitimately sit inside the pool by design, and
// CheckPoolOverlap will need a scoped exception for those specific
// interfaces — see the TODO on CheckPoolOverlap. That exception does not
// exist yet, so #962 must add it deliberately rather than disabling the
// check wholesale.
//
// # Composition requirements
//
// netguard must wrap the raw kernel net.Listener directly. Never layer it on
// top of a PROXY-protocol listener (or anything else that rewrites
// LocalAddr/RemoteAddr from a trusted proxy's own framing) — this guard's
// entire premise is that those two addresses are kernel-assigned, and a
// layer that substitutes application-supplied values for them would make
// both checks trust exactly what they're supposed to distrust. Likewise, any
// future separate listener on this process (a metrics or pprof endpoint, for
// example) must itself be wrapped with netguard, or simply bound to loopback
// only — a bare additional net.Listen call bypasses this guard entirely,
// since it is a per-listener wrapper, not a process-wide firewall.
package netguard

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"
)

// refusalLogInterval bounds how often a refusal is logged, per reason. A
// plugin container hammering the guard must not be able to flood the log —
// the count accumulates between reports and is flushed with the next one, so
// no refusal goes uncounted, only unlogged individually. Per-reason (rather
// than one shared window) means a burst of, say, remote_addr_in_pool
// refusals can't delay or dilute the report for an unrelated
// local_addr_link_local burst happening at the same time.
const refusalLogInterval = 30 * time.Second

// linkLocalV6 is fe80::/10, the IPv6 link-local unicast range. Operator API
// traffic never legitimately arrives over it.
var linkLocalV6 = netip.MustParsePrefix("fe80::/10")

// refusalReason names why Accept closed a connection, both for the log
// line's "reason" attribute and for whatever a Wrap caller's OnRefuse hook
// does with it (main.go labels a metric with it).
type refusalReason string

const (
	reasonLocalUnparseable refusalReason = "local_addr_unparseable"
	reasonLocalInPool      refusalReason = "local_addr_in_pool"
	reasonRemoteInPool     refusalReason = "remote_addr_in_pool"
	reasonLocalLinkLocal   refusalReason = "local_addr_link_local"
	reasonRemoteLinkLocal  refusalReason = "remote_addr_link_local"
)

// Listener wraps a net.Listener and closes, without a response of any kind,
// every accepted connection that the guard's checks refuse (see the package
// doc for exactly which checks those are and why).
//
// Accept never returns a connection it has refused, and it never spins: a
// refusal loops to the next Accept on the underlying listener, and any error
// from that Accept (temporary, or the listener being closed for shutdown) is
// returned exactly as the underlying listener produced it. That keeps
// http.Server's own temporary-error backoff and Shutdown behavior intact —
// this wrapper changes which connections are accepted, not how Accept errors
// are handled.
type Listener struct {
	net.Listener
	denyPool netip.Prefix
	logger   *slog.Logger
	onRefuse func(reason string)

	mu           sync.Mutex
	refused      uint64 // lifetime count, for callers that want to assert on it
	windowCounts map[refusalReason]uint64
	lastReportAt map[refusalReason]time.Time
}

// timeNow is overridden in tests so the log-interval logic runs on a fake
// clock instead of wall-clock timing.
var timeNow = time.Now

// Option configures optional Listener behavior at Wrap time.
type Option func(*Listener)

// WithOnRefuse registers a callback invoked (synchronously, on whatever
// goroutine called Accept) every time a connection is refused, with the
// reason string. It exists so a caller can hook the guard into its own
// metrics without this package importing a metrics registry itself — main.go
// uses it to increment gleipnir_operator_api_refused_total, keeping netguard
// a strict leaf package. f must not block or panic; it runs inline in
// Accept's hot path.
func WithOnRefuse(f func(reason string)) Option {
	return func(l *Listener) { l.onRefuse = f }
}

// Wrap returns ln wrapped so Accept refuses any connection whose local or
// remote address falls inside denyPool, or in fe80::/10 (see the package
// doc). denyPool must be a valid prefix — an invalid one would silently turn
// the guard into a no-op, so that is a startup error rather than a runtime
// one. A nil logger falls back to slog.Default().
func Wrap(ln net.Listener, denyPool netip.Prefix, logger *slog.Logger, opts ...Option) (*Listener, error) {
	if ln == nil {
		return nil, errors.New("netguard: listener is nil")
	}
	if !denyPool.IsValid() {
		return nil, errors.New("netguard: deny pool is not a valid CIDR")
	}
	if logger == nil {
		logger = slog.Default()
	}
	l := &Listener{
		Listener:     ln,
		denyPool:     denyPool.Masked(),
		logger:       logger,
		windowCounts: make(map[refusalReason]uint64),
		lastReportAt: make(map[refusalReason]time.Time),
	}
	for _, opt := range opts {
		opt(l)
	}
	return l, nil
}

// Installed reports whether ln is a netguard-wrapped listener. Exposed so a
// caller composing several listeners can assert the guard is actually in the
// chain rather than trusting that whoever built it remembered to call Wrap.
// See the package doc's "Composition requirements" for what Installed cannot
// catch — a proxy-protocol layer, or a second raw listener elsewhere in the
// process, both look identical to Installed and neither is safe.
func Installed(ln net.Listener) bool {
	_, ok := ln.(*Listener)
	return ok
}

// Refused returns the lifetime count of connections this listener has
// refused, across all reasons. It is for tests and diagnostics, not a rate —
// the log line is what's rate-limited, not this counter, and a per-reason
// breakdown is available via WithOnRefuse.
func (l *Listener) Refused() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.refused
}

// Accept refuses any connection the guard's checks reject and otherwise
// behaves exactly like the wrapped listener's Accept.
func (l *Listener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			// Propagate verbatim: http.Server already knows how to treat a
			// temporary error (brief backoff, retry) versus a permanent one
			// (net.ErrClosed on Shutdown/Close, which must stop Serve's
			// loop). Reinterpreting either case here would break that.
			return nil, err
		}

		local, remote := conn.LocalAddr(), conn.RemoteAddr()
		reason, refuse := l.classify(local, remote)
		if !refuse {
			return conn, nil
		}

		setLingerZero(conn)
		_ = conn.Close()
		l.recordRefusal(reason, local, remote)
	}
}

// classify decides whether a connection must be refused, and why.
//
// LocalAddr is checked first because it is the guard's primary, hardest-to-
// influence signal (see the package doc); an unparseable LocalAddr fails
// closed rather than falling through to the RemoteAddr checks, because a
// LocalAddr the guard cannot understand is exactly the case it cannot prove
// safe. RemoteAddr is only ever consulted to add a refusal, never to
// withdraw one the LocalAddr checks already made.
func (l *Listener) classify(local, remote net.Addr) (refusalReason, bool) {
	localIP, ok := addrIP(local)
	if !ok {
		return reasonLocalUnparseable, true
	}
	if linkLocalV6.Contains(localIP) {
		return reasonLocalLinkLocal, true
	}
	if l.denyPool.Contains(localIP.Unmap()) {
		return reasonLocalInPool, true
	}

	if remoteIP, ok := addrIP(remote); ok {
		if linkLocalV6.Contains(remoteIP) {
			return reasonRemoteLinkLocal, true
		}
		if l.denyPool.Contains(remoteIP.Unmap()) {
			return reasonRemoteInPool, true
		}
	}

	return "", false
}

// addrIP extracts the netip.Addr a net.Addr carries, in its original form
// (not unmapped) so a genuine IPv6 address still compares correctly against
// an IPv6 prefix like fe80::/10. Callers that need to compare against the
// (IPv4) deny pool call .Unmap() themselves.
//
// Handles both real *net.TCPAddr values and the host:port string form a test
// double might return — and, defensively, a nil net.Addr or a typed-nil
// *net.TCPAddr (an interface holding a nil pointer), neither of which any
// real net.Conn implementation should produce, but a crash here would take
// down Accept's whole loop rather than just refuse one odd connection, so
// this fails closed (unparseable, not a panic) instead of trusting that
// assumption.
func addrIP(addr net.Addr) (netip.Addr, bool) {
	if addr == nil {
		return netip.Addr{}, false
	}
	if tcpAddr, ok := addr.(*net.TCPAddr); ok {
		if tcpAddr == nil {
			return netip.Addr{}, false
		}
		ip, ok := netip.AddrFromSlice(tcpAddr.IP)
		if !ok {
			return netip.Addr{}, false
		}
		return ip, true
	}

	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return netip.Addr{}, false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip, true
}

// formatAddr renders a net.Addr for a log line without panicking on a nil
// net.Addr or a typed-nil *net.TCPAddr — calling .String() directly on
// either panics, because String() assumes a concrete, non-nil receiver. Used
// everywhere this package logs an address, since the whole point of a
// refusal log line is to describe the connection that caused it, including
// in the addrIP-returned-false case where that connection's address is
// exactly the thing that couldn't be parsed.
func formatAddr(addr net.Addr) string {
	if addr == nil {
		return "<nil>"
	}
	if tcpAddr, ok := addr.(*net.TCPAddr); ok && tcpAddr == nil {
		return "<nil>"
	}
	return addr.String()
}

// setLingerZero makes a refused TCP connection's Close send an immediate RST
// instead of the default graceful FIN sequence. The refusal already carries
// no response of any kind, and skipping the FIN/TIME_WAIT dance means a
// plugin repeatedly probing the guard cannot pile up sockets in TIME_WAIT on
// Gleipnir's side. Not every net.Conn is a *net.TCPConn (test doubles are
// not), so this is a best-effort cast, not a requirement.
func setLingerZero(conn net.Conn) {
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetLinger(0)
	}
}

// refusalMessage renders a reason-specific log message. remote_addr_in_pool
// gets an operator-facing hint (see the package doc's RemoteAddr section):
// that reason is the one case where the refused party might be a legitimate
// operator client whose own address happens to overlap the pool, rather than
// an actual plugin instance, and the fix is a pool choice, not a bug report.
func refusalMessage(reason refusalReason, local, remote net.Addr) string {
	switch reason {
	case reasonLocalUnparseable:
		return "netguard: refused a connection whose local address could not be parsed"
	case reasonLocalInPool:
		return fmt.Sprintf("netguard: refused a connection arriving on local address %s, which is inside GLEIPNIR_PLUGIN_SUBNET_POOL", formatAddr(local))
	case reasonRemoteInPool:
		return fmt.Sprintf("client source %s is inside GLEIPNIR_PLUGIN_SUBNET_POOL; if this is a legitimate operator, choose a non-overlapping pool", formatAddr(remote))
	case reasonLocalLinkLocal:
		return fmt.Sprintf("netguard: refused a connection arriving on link-local (fe80::/10) local address %s", formatAddr(local))
	case reasonRemoteLinkLocal:
		return fmt.Sprintf("netguard: refused a connection whose client source %s is link-local (fe80::/10)", formatAddr(remote))
	default:
		return "netguard: refused a connection"
	}
}

// recordRefusal tallies a refusal and logs at most once per
// refusalLogInterval *per reason*, with the accumulated count for that
// reason since its last line — the same coalescing shape
// internal/plugin/logcapture uses for dropped output, applied here per
// refusal reason instead of globally.
func (l *Listener) recordRefusal(reason refusalReason, local, remote net.Addr) {
	now := timeNow()

	l.mu.Lock()
	l.refused++
	l.windowCounts[reason]++
	n := l.windowCounts[reason]
	report := now.Sub(l.lastReportAt[reason]) >= refusalLogInterval
	if report {
		l.lastReportAt[reason] = now
		l.windowCounts[reason] = 0
	}
	l.mu.Unlock()

	if l.onRefuse != nil {
		l.onRefuse(string(reason))
	}

	if !report {
		return
	}
	l.logger.Warn(refusalMessage(reason, local, remote),
		"reason", string(reason),
		"count", n,
		"deny_pool", l.denyPool.String(),
		"sample_local_addr", formatAddr(local),
		"sample_remote_addr", formatAddr(remote),
	)
}
