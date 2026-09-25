package hostclient

import (
	"net/http"
	"testing"
	"time"
)

// TestNoProxyTransport_DisablesProxying and TestNew_DefaultsToNoProxyTransport
// prove New()'s default client can never route a host-endpoint call through a
// proxy (#955 security review finding 3).
//
// This is deliberately a white-box test (package hostclient, not
// hostclient_test): an env-var-driven black-box test — set HTTP_PROXY to an
// address nothing listens on and assert the call still reaches a fake
// server — looks like the obvious proof, but net/http caches its parsed
// proxy environment process-wide behind a sync.Once the first time anything
// in the test binary consults it. Whichever env state existed at that first
// call sticks for the rest of the process, which makes such a test pass
// whether or not the client actually consults HTTP_PROXY at all — asserting
// on the Transport itself is the only way this property does not depend on
// test execution order.
func TestNoProxyTransport_DisablesProxying(t *testing.T) {
	tr, ok := noProxyTransport.(*http.Transport)
	if !ok {
		t.Fatalf("noProxyTransport = %T, want *http.Transport", noProxyTransport)
	}
	if tr.Proxy != nil {
		t.Error("noProxyTransport.Proxy is set; host-endpoint calls would honor HTTP_PROXY/HTTPS_PROXY/NO_PROXY from the process environment")
	}
}

// TestNoProxyTransport_KeepsDefaultTransportTimeouts proves noProxyTransport
// is a CLONE of http.DefaultTransport, not a bare &http.Transport{} (#955
// security re-review round 2 item 6): the zero value drops the dial, TLS
// handshake, and idle-connection timeouts DefaultTransport sets, which would
// trade a proxy hole for a client that can hang forever on a wedged host
// endpoint -- one failure mode for another, not a fix.
func TestNoProxyTransport_KeepsDefaultTransportTimeouts(t *testing.T) {
	tr, ok := noProxyTransport.(*http.Transport)
	if !ok {
		t.Fatalf("noProxyTransport = %T, want *http.Transport", noProxyTransport)
	}
	want := http.DefaultTransport.(*http.Transport)
	if tr.TLSHandshakeTimeout != want.TLSHandshakeTimeout {
		t.Errorf("TLSHandshakeTimeout = %v, want %v (http.DefaultTransport's)", tr.TLSHandshakeTimeout, want.TLSHandshakeTimeout)
	}
	if tr.IdleConnTimeout != want.IdleConnTimeout {
		t.Errorf("IdleConnTimeout = %v, want %v (http.DefaultTransport's)", tr.IdleConnTimeout, want.IdleConnTimeout)
	}
	if tr.ExpectContinueTimeout != want.ExpectContinueTimeout {
		t.Errorf("ExpectContinueTimeout = %v, want %v (http.DefaultTransport's)", tr.ExpectContinueTimeout, want.ExpectContinueTimeout)
	}
	if tr.DialContext == nil {
		t.Error("DialContext is nil; a bare &http.Transport{} has no dial timeout at all")
	}
}

// TestNoProxyTransportFrom_FallsBackWhenNotAnHTTPTransport exercises the
// comma-ok fallback branch (#955 security re-review round 3 item 5): if the
// "default transport" handed in is not a *http.Transport (a future Go
// release changing http.DefaultTransport's concrete type, or a caller
// passing something else entirely), noProxyTransportFrom must not panic --
// it falls back to an explicit *http.Transport carrying real dial/TLS/idle
// timeouts, not the zero value.
func TestNoProxyTransportFrom_FallsBackWhenNotAnHTTPTransport(t *testing.T) {
	tr := noProxyTransportFrom(fakeRoundTripper{})

	if tr.Proxy != nil {
		t.Error("fallback transport proxies; want Proxy: nil")
	}
	if tr.DialContext == nil {
		t.Error("fallback transport has no DialContext; want an explicit dial timeout")
	}
	if tr.TLSHandshakeTimeout == 0 {
		t.Error("fallback transport has no TLSHandshakeTimeout")
	}
	if tr.IdleConnTimeout == 0 {
		t.Error("fallback transport has no IdleConnTimeout")
	}
	if tr.ExpectContinueTimeout == 0 {
		t.Error("fallback transport has no ExpectContinueTimeout")
	}
}

// fakeRoundTripper is any http.RoundTripper implementation that is NOT
// *http.Transport, to drive noProxyTransportFrom's fallback branch.
type fakeRoundTripper struct{}

func (fakeRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, nil }

func TestNoProxyTransportFrom_ClonesARealHTTPTransport(t *testing.T) {
	base := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 5 * time.Second}
	tr := noProxyTransportFrom(base)

	if tr.Proxy != nil {
		t.Error("cloned transport proxies; want Proxy: nil")
	}
	if tr.TLSHandshakeTimeout != base.TLSHandshakeTimeout {
		t.Errorf("TLSHandshakeTimeout = %v, want the base transport's %v", tr.TLSHandshakeTimeout, base.TLSHandshakeTimeout)
	}
	if tr == base {
		t.Error("noProxyTransportFrom mutated the base transport in place instead of cloning it")
	}
	if base.Proxy == nil {
		t.Error("the base transport's own Proxy was mutated; noProxyTransportFrom must clone, not share")
	}
}

func TestNew_DefaultsToNoProxyTransport(t *testing.T) {
	c, err := New(WithBaseURL("http://example.invalid"), WithToken("t"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.httpClient.Transport != noProxyTransport {
		t.Errorf("New()'s default client's Transport = %#v, want noProxyTransport", c.httpClient.Transport)
	}
}

// WithHTTPClient still lets an author opt back into proxying — the point is
// that New() never does it silently, not that it is forbidden.
func TestWithHTTPClient_OverridesTheDefaultTransport(t *testing.T) {
	custom := &http.Client{}
	c, err := New(WithBaseURL("http://example.invalid"), WithToken("t"), WithHTTPClient(custom))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.httpClient != custom {
		t.Error("WithHTTPClient did not override the default client")
	}
}
