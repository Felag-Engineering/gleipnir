package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DenyReason names why a connection was refused. It is a closed vocabulary
// because it becomes a metric label and an audit field — an operator filtering
// "how often is this plugin asking for things it was never granted" needs the
// reasons to be countable, not prose.
type DenyReason string

const (
	// DenyUnknownInstance — the connection arrived on an address that maps to
	// no instance. Fail closed: an unattributable connection cannot be checked
	// against anyone's grants.
	DenyUnknownInstance DenyReason = "unknown_instance"

	// DenyNotGranted — the host is not on this instance's consented list.
	DenyNotGranted DenyReason = "not_granted"

	// DenyIPLiteral — the target was an address rather than a name. Grants are
	// domains, so a literal could never match one; refusing it explicitly
	// removes the temptation to "just resolve it and compare".
	DenyIPLiteral DenyReason = "ip_literal"

	// DenyPrivateAddress — the target resolved into private, loopback,
	// link-local, unique-local, or unspecified space. This is the east-west
	// guard: without it the proxy would happily dial a sibling instance's MCP
	// endpoint, undoing the per-instance network isolation through the very
	// component meant to enforce it.
	DenyPrivateAddress DenyReason = "private_address"

	// DenyUnresolvable — the target did not resolve.
	DenyUnresolvable DenyReason = "unresolvable"

	// DenyMalformed — the request could not be understood as a proxied request.
	DenyMalformed DenyReason = "malformed_request"
)

// Resolver maps the local (gateway) address a connection arrived on to the
// instance that owns that network, and to its consented allowlist.
//
// The local address is the identity, not the peer address: the kernel picks it
// from which interface the packet arrived on, so a plugin cannot claim another
// instance's grants by lying about where it is from. See
// docs/developer/egress-containment.md.
type Resolver interface {
	// InstanceForGateway returns the instance ID and allowlist for the network
	// whose gateway is localIP. ok is false when the address belongs to no
	// managed network.
	InstanceForGateway(localIP net.IP) (instanceID string, list Allowlist, ok bool)
}

// Auditor records refusals durably. Optional — a nil Auditor means the proxy
// logs and counts but writes nothing.
type Auditor interface {
	EgressDenied(ctx context.Context, instanceID, host string, reason DenyReason)
}

// Config wires a Proxy.
type Config struct {
	// Resolver maps arrival address to instance. Required.
	Resolver Resolver

	// Auditor records refusals. Optional.
	Auditor Auditor

	// DialTimeout bounds the upstream connection attempt; zero uses a default.
	DialTimeout time.Duration

	// IdleTimeout bounds how long a tunnel may sit with no traffic; zero means
	// no idle bound (the connection still dies with its peer).
	IdleTimeout time.Duration

	// Lookup resolves a hostname. Injected so the private-address guard is
	// testable without DNS; nil uses the system resolver.
	Lookup func(ctx context.Context, host string) ([]net.IP, error)
}

// Proxy is the single way out of a plugin's internal-only network.
//
// It speaks the forward-proxy subset that HTTP clients use when handed
// HTTPS_PROXY: `CONNECT host:port` for TLS, and absolute-form request URIs for
// plaintext HTTP. TLS is tunnelled, never terminated — the allow decision is
// made from the plaintext CONNECT line before any bytes are copied, and after
// that the proxy is deliberately blind.
type Proxy struct {
	resolver    Resolver
	auditor     Auditor
	dialTimeout time.Duration
	idleTimeout time.Duration
	lookup      func(ctx context.Context, host string) ([]net.IP, error)

	mu      sync.Mutex
	denials map[DenyReason]int
	allowed int
}

const (
	defaultDialTimeout = 10 * time.Second
	defaultProxyPort   = "443"
)

func New(cfg Config) (*Proxy, error) {
	if cfg.Resolver == nil {
		return nil, fmt.Errorf("egress: Resolver is required")
	}
	p := &Proxy{
		resolver:    cfg.Resolver,
		auditor:     cfg.Auditor,
		dialTimeout: cfg.DialTimeout,
		idleTimeout: cfg.IdleTimeout,
		lookup:      cfg.Lookup,
		denials:     make(map[DenyReason]int),
	}
	if p.dialTimeout <= 0 {
		p.dialTimeout = defaultDialTimeout
	}
	if p.lookup == nil {
		p.lookup = systemLookup
	}
	return p, nil
}

func systemLookup(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, len(addrs))
	for i, a := range addrs {
		ips[i] = a.IP
	}
	return ips, nil
}

// ServeHTTP implements the proxy. It handles CONNECT itself (hijacking the
// connection to tunnel bytes) and forwards absolute-form plain HTTP requests.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	instanceID, list, ok := p.identify(r)
	if !ok {
		p.refuse(r.Context(), w, r, "", targetHostOf(r), DenyUnknownInstance)
		return
	}

	host := targetHostOf(r)
	if host == "" {
		p.refuse(r.Context(), w, r, instanceID, "", DenyMalformed)
		return
	}

	if reason, allowed := p.check(r.Context(), host, list); !allowed {
		p.refuse(r.Context(), w, r, instanceID, host, reason)
		return
	}

	if r.Method == http.MethodConnect {
		p.tunnel(w, r, instanceID, host)
		return
	}
	p.forward(w, r, instanceID, host)
}

// identify establishes which instance is calling, from the address the
// connection arrived on.
func (p *Proxy) identify(r *http.Request) (string, Allowlist, bool) {
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return "", Allowlist{}, false
	}
	host, _, err := net.SplitHostPort(local.String())
	if err != nil {
		host = local.String()
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", Allowlist{}, false
	}
	return p.resolver.InstanceForGateway(ip)
}

// check runs the full decision: grant match first, then the address guard.
//
// Order matters for what an operator sees. Checking the grant first means an
// ungranted host is reported as ungranted rather than as whatever its addresses
// happen to be — the operator's actual problem is the missing consent.
func (p *Proxy) check(ctx context.Context, host string, list Allowlist) (DenyReason, bool) {
	if net.ParseIP(host) != nil {
		return DenyIPLiteral, false
	}
	if !list.Allows(host) {
		return DenyNotGranted, false
	}

	// The east-west guard, applied to a host that IS granted: a consented name
	// resolving into private space is either a mistake or a rebinding attempt,
	// and either way dialing it would put Gleipnir's own address in front of an
	// address the plugin was never supposed to reach.
	lookupCtx, cancel := context.WithTimeout(ctx, p.dialTimeout)
	defer cancel()
	ips, err := p.lookup(lookupCtx, host)
	if err != nil {
		return DenyUnresolvable, false
	}
	if len(ips) == 0 {
		return DenyUnresolvable, false
	}
	for _, ip := range ips {
		if addressIsContained(ip) {
			return DenyPrivateAddress, false
		}
	}
	return "", true
}

// addressIsContained is the package's injectable guard, following the same
// pattern the codebase uses for clocks. Exactly one test swaps it — the one
// whose upstream genuinely lives on loopback — and it must not run in parallel
// while swapped. Every other test exercises the guard as shipped.
var addressIsContained = isContainedAddress

// isContainedAddress reports whether an address is one the proxy must never
// dial on a plugin's behalf.
//
// Deliberately broader than "private": loopback reaches the host itself,
// link-local reaches cloud metadata services, and unspecified is a way of
// asking for "whatever the stack picks". Every one of them is inside the
// boundary the plugin is contained by.
func isContainedAddress(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast()
}

// targetHostOf extracts the hostname (no port) a request is aimed at.
func targetHostOf(r *http.Request) string {
	target := r.Host
	if r.Method == http.MethodConnect {
		target = r.URL.Host
		if target == "" {
			target = r.Host
		}
	} else if r.URL != nil && r.URL.Host != "" {
		target = r.URL.Host
	}
	if target == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(target); err == nil {
		return host
	}
	return target
}

// tunnel hijacks the connection and copies bytes in both directions. Nothing
// past this point inspects the stream — that is what makes it TLS passthrough
// rather than interception.
func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request, instanceID, host string) {
	addr := r.URL.Host
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(host, defaultProxyPort)
	}

	upstream, err := p.dial(r.Context(), addr)
	if err != nil {
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "proxy cannot tunnel on this transport", http.StatusInternalServerError)
		return
	}
	client, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, "proxy cannot tunnel", http.StatusInternalServerError)
		return
	}
	defer client.Close()

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}

	p.countAllowed()
	slog.DebugContext(r.Context(), "plugin egress allowed",
		"instance_id", instanceID, "host", host, "mode", "connect")

	if p.idleTimeout > 0 {
		deadline := time.Now().Add(p.idleTimeout)
		_ = client.SetDeadline(deadline)
		_ = upstream.SetDeadline(deadline)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(upstream, client); closeWrite(upstream) }()
	go func() { defer wg.Done(); _, _ = io.Copy(client, upstream); closeWrite(client) }()
	wg.Wait()
}

// closeWrite half-closes a connection so the peer sees EOF rather than waiting
// out an idle timeout on a stream the other side has finished with.
func closeWrite(conn net.Conn) {
	type writeCloser interface{ CloseWrite() error }
	if wc, ok := conn.(writeCloser); ok {
		_ = wc.CloseWrite()
	}
}

// forward proxies a plaintext HTTP request. Present because a plugin handed
// HTTP_PROXY will use absolute-form URIs for http:// destinations, and refusing
// them would silently break a granted destination that happens not to use TLS.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, instanceID, host string) {
	outbound := r.Clone(r.Context())
	outbound.RequestURI = ""
	// Hop-by-hop headers belong to the connection, not the message; forwarding
	// Proxy-Authorization in particular would leak a credential meant for us.
	for _, h := range []string{"Proxy-Connection", "Proxy-Authorization", "Connection", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		outbound.Header.Del(h)
	}

	client := &http.Client{
		Timeout: p.dialTimeout,
		Transport: &http.Transport{
			DialContext:           func(ctx context.Context, _, addr string) (net.Conn, error) { return p.dial(ctx, addr) },
			DisableKeepAlives:     true,
			ResponseHeaderTimeout: p.dialTimeout,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// Redirects are not followed here: the next hop is a destination
			// the plugin never asked for and this proxy never checked. The
			// plugin's own client follows it, which sends it back through this
			// decision.
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(outbound)
	if err != nil {
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	p.countAllowed()
	slog.DebugContext(r.Context(), "plugin egress allowed",
		"instance_id", instanceID, "host", host, "mode", "http")

	for key, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// dial connects upstream, re-checking the resolved address.
//
// The check in this package's `check` ran against the hostname's addresses a
// moment ago; this one runs against what is actually being connected to. The
// duplication is the point — between the two there is a window a rebinding
// resolver could use, and this is the side that decides where bytes go.
func (p *Proxy) dial(ctx context.Context, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("egress: malformed upstream address %q: %w", addr, err)
	}

	lookupCtx, cancel := context.WithTimeout(ctx, p.dialTimeout)
	defer cancel()
	ips, err := p.lookup(lookupCtx, host)
	if err != nil {
		return nil, fmt.Errorf("egress: resolving %q: %w", host, err)
	}

	dialer := net.Dialer{Timeout: p.dialTimeout}
	var lastErr error = errors.New("egress: no usable address")
	for _, ip := range ips {
		if addressIsContained(ip) {
			// Never dial inward, even if the earlier check passed: this is the
			// last point before bytes move.
			lastErr = fmt.Errorf("egress: %s resolves into contained address space", host)
			continue
		}
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// refuse answers the client, logs, counts, and audits.
//
// The body names the host and the reason on purpose: the plugin's own error
// message is the first place a developer looks, and "403 from the proxy" with
// no host is the undebuggable denial this design exists to avoid.
func (p *Proxy) refuse(ctx context.Context, w http.ResponseWriter, r *http.Request, instanceID, host string, reason DenyReason) {
	p.countDenied(reason)

	slog.WarnContext(ctx, "plugin egress denied",
		"instance_id", instanceID, "host", host, "reason", string(reason), "method", r.Method)

	if p.auditor != nil {
		p.auditor.EgressDenied(ctx, instanceID, host, reason)
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(w, "gleipnir: egress to %q refused (%s)\n", host, reason)
}

func (p *Proxy) countDenied(reason DenyReason) {
	p.mu.Lock()
	p.denials[reason]++
	p.mu.Unlock()
	egressDenied.WithLabelValues(string(reason)).Inc()
}

func (p *Proxy) countAllowed() {
	p.mu.Lock()
	p.allowed++
	p.mu.Unlock()
	egressAllowed.Inc()
}

// Counters returns the in-process tallies. Test-facing, and cheap enough to
// leave in: the Prometheus counters are the operator surface.
func (p *Proxy) Counters() (allowed int, denied map[DenyReason]int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[DenyReason]int, len(p.denials))
	for k, v := range p.denials {
		out[k] = v
	}
	return p.allowed, out
}

// ProxyEnv returns the environment entries that point a plugin container at the
// proxy, given the gateway address of its network.
//
// NO_PROXY is set to the empty string deliberately rather than left unset: a
// stray inherited value from a base image would be a hole in the containment,
// and "" is the only value that means "nothing bypasses".
func ProxyEnv(gateway net.IP, port int) []string {
	url := fmt.Sprintf("http://%s", net.JoinHostPort(gateway.String(), fmt.Sprint(port)))
	return []string{
		"HTTP_PROXY=" + url,
		"HTTPS_PROXY=" + url,
		// Lower-case variants: Go's http.ProxyFromEnvironment reads both, but
		// curl and several language runtimes only read the lower-case form.
		"http_proxy=" + url,
		"https_proxy=" + url,
		"NO_PROXY=",
		"no_proxy=",
	}
}

// StripProxyEnv removes any proxy variables an image or operator config may
// already carry, so ProxyEnv's values are the only ones present.
func StripProxyEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToLower(key) {
		case "http_proxy", "https_proxy", "no_proxy", "all_proxy":
			continue
		}
		out = append(out, entry)
	}
	return out
}
