package egress

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- fixtures ---------------------------------------------------------------

// stubResolver maps every connection to one instance, or to none.
type stubResolver struct {
	instanceID string
	list       Allowlist
	known      bool
}

func (s stubResolver) InstanceForGateway(net.IP) (string, Allowlist, bool) {
	if !s.known {
		return "", Allowlist{}, false
	}
	return s.instanceID, s.list, true
}

// perGatewayResolver maps by the arrival address, which is what production
// does. Used for the identity test.
type perGatewayResolver map[string]struct {
	instanceID string
	list       Allowlist
}

func (m perGatewayResolver) InstanceForGateway(ip net.IP) (string, Allowlist, bool) {
	entry, ok := m[ip.String()]
	if !ok {
		return "", Allowlist{}, false
	}
	return entry.instanceID, entry.list, true
}

type recordingAuditor struct {
	mu      sync.Mutex
	records []auditRecord
}

type auditRecord struct {
	instanceID string
	host       string
	reason     DenyReason
}

func (a *recordingAuditor) EgressDenied(_ context.Context, instanceID, host string, reason DenyReason) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = append(a.records, auditRecord{instanceID, host, reason})
}

func (a *recordingAuditor) all() []auditRecord {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]auditRecord(nil), a.records...)
}

// fixedLookup resolves every name to the same addresses.
func fixedLookup(ips ...string) func(context.Context, string) ([]net.IP, error) {
	return func(context.Context, string) ([]net.IP, error) {
		out := make([]net.IP, len(ips))
		for i, s := range ips {
			out[i] = net.ParseIP(s)
		}
		return out, nil
	}
}

// startProxy runs the proxy on a loopback listener and returns its address.
func startProxy(t *testing.T, cfg Config) (*Proxy, string) {
	t.Helper()
	proxy, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(proxy)
	t.Cleanup(srv.Close)
	return proxy, srv.URL
}

// proxyClient returns an HTTP client that routes everything through the proxy.
func proxyClient(t *testing.T, proxyURL string) *http.Client {
	t.Helper()
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatalf("parse proxy url: %v", err)
	}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(parsed),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test upstream uses a self-signed cert
		},
	}
}

// --- the grant decision -----------------------------------------------------

// A granted destination reaches its upstream, and the proxy is not in the TLS
// path: the upstream's own certificate is what the client validates against,
// which is only true if the proxy tunnelled rather than terminated.
func TestProxy_GrantedDestinationTunnelsTLS(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "upstream ok")
	}))
	t.Cleanup(upstream.Close)

	upstreamHost, upstreamPort := splitAddr(t, upstream.Listener.Addr().String())

	// The upstream genuinely lives on loopback, which the contained-address
	// guard refuses by design. Swap it for this one test — everything else in
	// this file exercises the guard as shipped. No t.Parallel while swapped.
	addressIsContained = func(net.IP) bool { return false }
	t.Cleanup(func() { addressIsContained = isContainedAddress })

	proxy, proxyURL := startProxy(t, Config{
		Resolver: stubResolver{instanceID: "inst-1", list: mustAllowlist(t, "granted.example.com"), known: true},
		Lookup:   fixedLookup(upstreamHost),
	})

	client := proxyClient(t, proxyURL)
	resp, err := client.Get("https://granted.example.com:" + upstreamPort + "/")
	if err != nil {
		t.Fatalf("granted request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "upstream ok" {
		t.Errorf("body = %q, want the upstream's own response", body)
	}

	allowed, denied := proxy.Counters()
	if allowed != 1 {
		t.Errorf("allowed = %d, want 1", allowed)
	}
	if len(denied) != 0 {
		t.Errorf("denials = %v, want none", denied)
	}
}

// The core refusal, and the one an operator will see most: a host nobody
// consented to.
func TestProxy_UngrantedDestinationIsRefused(t *testing.T) {
	auditor := &recordingAuditor{}
	proxy, proxyURL := startProxy(t, Config{
		Resolver: stubResolver{instanceID: "inst-1", list: mustAllowlist(t, "granted.example.com"), known: true},
		Auditor:  auditor,
		Lookup:   fixedLookup("93.184.216.34"),
	})

	client := proxyClient(t, proxyURL)
	_, err := client.Get("https://not-granted.example.com/")
	if err == nil {
		t.Fatal("ungranted request succeeded")
	}

	_, denied := proxy.Counters()
	if denied[DenyNotGranted] != 1 {
		t.Errorf("not_granted denials = %d, want 1 (all: %v)", denied[DenyNotGranted], denied)
	}

	records := auditor.all()
	if len(records) != 1 {
		t.Fatalf("audit records = %v, want 1", records)
	}
	// Silent denial is undebuggable — the record has to name the instance and
	// the host that was refused.
	if records[0].instanceID != "inst-1" || records[0].host != "not-granted.example.com" {
		t.Errorf("audit record = %+v", records[0])
	}
	if records[0].reason != DenyNotGranted {
		t.Errorf("reason = %q, want %q", records[0].reason, DenyNotGranted)
	}
}

// --- the east-west guard ----------------------------------------------------

// The trap this design has to avoid: the proxy sits on every instance network
// AND has ordinary egress, so a naive implementation would dial a sibling
// instance's MCP endpoint on request — reopening east-west traffic through the
// component meant to contain it.
func TestProxy_RefusesSiblingInstanceAndOtherContainedSpace(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   DenyReason
	}{
		{"sibling instance by address", "https://10.83.4.5:8080/", DenyIPLiteral},
		{"the host itself", "https://127.0.0.1:8080/", DenyIPLiteral},
		{"private space by address", "https://192.168.1.10/", DenyIPLiteral},
		{"cloud metadata by address", "https://169.254.169.254/latest/meta-data/", DenyIPLiteral},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proxy, proxyURL := startProxy(t, Config{
				// Even with a permissive-looking list, an address literal can
				// never match a domain grant.
				Resolver: stubResolver{instanceID: "inst-1", list: mustAllowlist(t, "*.example.com"), known: true},
				Lookup:   fixedLookup("93.184.216.34"),
			})

			client := proxyClient(t, proxyURL)
			if _, err := client.Get(tc.target); err == nil {
				t.Fatal("request to contained address space succeeded")
			}
			_, denied := proxy.Counters()
			if denied[tc.want] != 1 {
				t.Errorf("denials = %v, want one %q", denied, tc.want)
			}
		})
	}
}

// A GRANTED name that resolves inward is refused too. This is the rebinding
// case — deliberate or accidental, the outcome must be the same, because a
// grant for a public name is not consent to reach the network the plugin is
// contained on.
func TestProxy_RefusesGrantedNameResolvingIntoContainedSpace(t *testing.T) {
	tests := []struct {
		name string
		ips  []string
	}{
		{"sibling subnet", []string{"10.83.4.5"}},
		{"loopback", []string{"127.0.0.1"}},
		{"link-local metadata", []string{"169.254.169.254"}},
		{"unspecified", []string{"0.0.0.0"}},
		// One bad address among good ones is still a refusal: a resolver that
		// returns both is a resolver being used to smuggle one in.
		{"mixed public and private", []string{"93.184.216.34", "10.83.4.5"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proxy, proxyURL := startProxy(t, Config{
				Resolver: stubResolver{instanceID: "inst-1", list: mustAllowlist(t, "granted.example.com"), known: true},
				Lookup:   fixedLookup(tc.ips...),
			})

			client := proxyClient(t, proxyURL)
			if _, err := client.Get("https://granted.example.com/"); err == nil {
				t.Fatal("granted name resolving inward succeeded")
			}
			_, denied := proxy.Counters()
			if denied[DenyPrivateAddress] != 1 {
				t.Errorf("denials = %v, want one private_address", denied)
			}
		})
	}
}

func TestIsContainedAddress(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"93.184.216.34", false},
		{"1.1.1.1", false},
		{"2606:4700:4700::1111", false},

		{"127.0.0.1", true},
		{"::1", true},
		{"10.83.4.5", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true},
		{"fe80::1", true},
		{"0.0.0.0", true},
		{"224.0.0.1", true},
	}
	for _, tc := range tests {
		t.Run(tc.addr, func(t *testing.T) {
			if got := isContainedAddress(net.ParseIP(tc.addr)); got != tc.want {
				t.Errorf("isContainedAddress(%s) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
	if !isContainedAddress(nil) {
		t.Error("a nil address must be treated as contained")
	}
}

// --- identity ---------------------------------------------------------------

// Identity comes from the address the connection ARRIVED on, which the peer
// does not choose. A connection arriving on an address that maps to no instance
// fails closed rather than falling back to some default.
func TestProxy_UnknownGatewayFailsClosed(t *testing.T) {
	auditor := &recordingAuditor{}
	proxy, proxyURL := startProxy(t, Config{
		Resolver: stubResolver{known: false},
		Auditor:  auditor,
		Lookup:   fixedLookup("93.184.216.34"),
	})

	client := proxyClient(t, proxyURL)
	if _, err := client.Get("https://anything.example.com/"); err == nil {
		t.Fatal("unattributable connection succeeded")
	}
	_, denied := proxy.Counters()
	if denied[DenyUnknownInstance] != 1 {
		t.Errorf("denials = %v, want one unknown_instance", denied)
	}
	if records := auditor.all(); len(records) != 1 || records[0].instanceID != "" {
		t.Errorf("audit records = %+v, want one with no instance", records)
	}
}

// Two instances, two gateways, two allowlists: each gets its own answer for the
// same host. This is the property that makes per-instance grants mean anything.
func TestProxy_AllowlistIsPerArrivalAddress(t *testing.T) {
	resolver := perGatewayResolver{
		"127.0.0.1": {instanceID: "inst-allowed", list: mustAllowlist(t, "shared.example.com")},
	}
	proxy, proxyURL := startProxy(t, Config{
		Resolver: resolver,
		Lookup:   fixedLookup("93.184.216.34"),
	})

	client := proxyClient(t, proxyURL)
	// Arrives on 127.0.0.1, which resolver maps to inst-allowed.
	_, err := client.Get("https://shared.example.com/")
	// The dial to 93.184.216.34 will fail in a sandbox; what matters is that it
	// was ALLOWED rather than refused.
	if err != nil && strings.Contains(err.Error(), "403") {
		t.Fatalf("granted host refused for the mapped instance: %v", err)
	}
	allowedCount, denied := proxy.Counters()
	if denied[DenyNotGranted] != 0 {
		t.Errorf("denials = %v, want no not_granted for the mapped instance", denied)
	}
	_ = allowedCount

	// Now the same host from an unmapped gateway.
	resolver2 := perGatewayResolver{"10.0.0.1": {instanceID: "inst-other", list: mustAllowlist(t, "shared.example.com")}}
	proxy2, proxyURL2 := startProxy(t, Config{Resolver: resolver2, Lookup: fixedLookup("93.184.216.34")})
	client2 := proxyClient(t, proxyURL2)
	if _, err := client2.Get("https://shared.example.com/"); err == nil {
		t.Fatal("connection from an unmapped gateway succeeded")
	}
	if _, denied := proxy2.Counters(); denied[DenyUnknownInstance] != 1 {
		t.Errorf("denials = %v, want one unknown_instance", denied)
	}
}

// --- environment ------------------------------------------------------------

// The env a container is handed. NO_PROXY is set to empty rather than left
// unset: an inherited value from a base image would be a hole, and "" is the
// only value meaning "nothing bypasses".
func TestProxyEnv(t *testing.T) {
	env := ProxyEnv(net.ParseIP("10.83.7.1"), 8118)

	want := map[string]string{
		"HTTP_PROXY":  "http://10.83.7.1:8118",
		"HTTPS_PROXY": "http://10.83.7.1:8118",
		"http_proxy":  "http://10.83.7.1:8118",
		"https_proxy": "http://10.83.7.1:8118",
		"NO_PROXY":    "",
		"no_proxy":    "",
	}
	got := map[string]string{}
	for _, entry := range env {
		k, v, _ := strings.Cut(entry, "=")
		got[k] = v
	}
	if len(got) != len(want) {
		t.Fatalf("env = %v, want %d entries", got, len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// An image or operator config carrying its own proxy variables must not be able
// to leave one in place — every one of them is a bypass.
func TestStripProxyEnv(t *testing.T) {
	got := StripProxyEnv([]string{
		"PATH=/usr/bin",
		"HTTP_PROXY=http://attacker:3128",
		"no_proxy=*",
		"ALL_PROXY=socks5://attacker:1080",
		"GLEIPNIR_INSTANCE_ID=inst-1",
		"HTTPS_PROXY=http://attacker:3128",
	})
	want := []string{"PATH=/usr/bin", "GLEIPNIR_INSTANCE_ID=inst-1"}
	if len(got) != len(want) {
		t.Fatalf("StripProxyEnv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// --- registry ---------------------------------------------------------------

func TestGatewayRegistry(t *testing.T) {
	reg := NewGatewayRegistry()
	list := mustAllowlist(t, "slack.com")

	reg.Set(net.ParseIP("10.83.1.1"), "inst-1", list)
	reg.Set(net.ParseIP("10.83.2.1"), "inst-2", Allowlist{})

	if id, got, ok := reg.InstanceForGateway(net.ParseIP("10.83.1.1")); !ok || id != "inst-1" || !got.Allows("slack.com") {
		t.Errorf("lookup of a mapped gateway = (%q, allows=%v, %v)", id, got.Allows("slack.com"), ok)
	}
	// An instance with no grants resolves, and grants nothing. Those are
	// different facts from "unknown gateway" and both matter.
	if id, got, ok := reg.InstanceForGateway(net.ParseIP("10.83.2.1")); !ok || id != "inst-2" || !got.Empty() {
		t.Errorf("lookup of a grantless instance = (%q, empty=%v, %v)", id, got.Empty(), ok)
	}
	if _, _, ok := reg.InstanceForGateway(net.ParseIP("10.83.9.1")); ok {
		t.Error("unmapped gateway resolved")
	}
	if _, _, ok := reg.InstanceForGateway(nil); ok {
		t.Error("nil address resolved")
	}

	reg.Remove(net.ParseIP("10.83.1.1"))
	if _, _, ok := reg.InstanceForGateway(net.ParseIP("10.83.1.1")); ok {
		t.Error("removed gateway still resolves")
	}

	// Replace is the level-triggered shape: an entry absent from the new world
	// disappears rather than lingering because nothing deleted it.
	reg.Replace(
		map[string]Allowlist{"10.83.5.1": list},
		map[string]string{"10.83.5.1": "inst-5"},
	)
	if reg.Len() != 1 {
		t.Errorf("Len() = %d after Replace, want 1", reg.Len())
	}
	if _, _, ok := reg.InstanceForGateway(net.ParseIP("10.83.2.1")); ok {
		t.Error("an entry absent from Replace's world survived")
	}
}

func TestGatewayOf(t *testing.T) {
	tests := []struct {
		subnet string
		want   string
	}{
		{"10.83.0.0/24", "10.83.0.1"},
		{"10.83.7.0/24", "10.83.7.1"},
		{"192.168.16.0/24", "192.168.16.1"},
	}
	for _, tc := range tests {
		t.Run(tc.subnet, func(t *testing.T) {
			got, err := GatewayOf(tc.subnet)
			if err != nil {
				t.Fatalf("GatewayOf: %v", err)
			}
			if got.String() != tc.want {
				t.Errorf("GatewayOf(%s) = %s, want %s", tc.subnet, got, tc.want)
			}
		})
	}
	if _, err := GatewayOf("not a cidr"); err == nil {
		t.Error("GatewayOf accepted a malformed subnet")
	}
}

func TestNew_RequiresResolver(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Error("New accepted a config with no Resolver")
	}
}

// --- helpers ----------------------------------------------------------------

func splitAddr(t *testing.T, addr string) (host, port string) {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	return host, port
}
