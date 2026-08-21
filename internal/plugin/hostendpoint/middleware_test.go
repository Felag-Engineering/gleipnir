package hostendpoint

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
)

// okHandler records whether it ran and what the middleware put in its
// context. "Rejected BEFORE the handler runs" is the property under test, so
// the handler itself is the probe.
type okHandler struct {
	called atomic.Bool
	gotID  atomic.Value // Identity
	gotCID atomic.Value // string
}

func (h *okHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.called.Store(true)
	if id, ok := IdentityFromContext(r.Context()); ok {
		h.gotID.Store(id)
	}
	if cid, ok := CallIDFromContext(r.Context()); ok {
		h.gotCID.Store(cid)
	}
	w.WriteHeader(http.StatusOK)
}

func TestRequireInstanceToken(t *testing.T) {
	reg := identity.New()
	token, err := reg.Issue("inst-a")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	resolver := RegistryResolver{Registry: reg}

	rejections := []struct {
		name   string
		header string
	}{
		{name: "no Authorization header", header: ""},
		{name: "wrong scheme", header: "Basic " + token},
		{name: "empty bearer credential", header: "Bearer   "},
		{name: "unknown token", header: "Bearer not-a-token"},
	}
	for _, tc := range rejections {
		t.Run(tc.name+" rejects before the handler runs", func(t *testing.T) {
			h := &okHandler{}
			mw := RequireInstanceToken(h, resolver)
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			mw.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
			if h.called.Load() {
				t.Fatal("handler ran despite the rejection — auth is not before-handler")
			}
		})
	}

	t.Run("a valid token attaches the identity", func(t *testing.T) {
		h := &okHandler{}
		mw := RequireInstanceToken(h, resolver)
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mw.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
		}
		id, _ := h.gotID.Load().(Identity)
		if id.InstanceID != "inst-a" {
			t.Errorf("identity = %+v, want InstanceID inst-a", id)
		}
	})

	t.Run("a superseded registry token rejects — Issue auto-revokes the prior one", func(t *testing.T) {
		// The v1.1 property "a killed-generation token cannot impersonate the
		// new generation", carried onto the HTTP transport.
		reg2 := identity.New()
		oldToken, err := reg2.Issue("inst-b")
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if _, err := reg2.Issue("inst-b"); err != nil {
			t.Fatalf("re-Issue: %v", err)
		}
		h := &okHandler{}
		mw := RequireInstanceToken(h, RegistryResolver{Registry: reg2})
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Authorization", "Bearer "+oldToken)
		w := httptest.NewRecorder()
		mw.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized || h.called.Load() {
			t.Fatalf("superseded token: status = %d, handler called = %v; want 401, false", w.Code, h.called.Load())
		}
	})
}

func TestWithCallID(t *testing.T) {
	cases := []struct {
		name     string
		values   []string
		want     string
		wantSeen bool
	}{
		{name: "single value is attached", values: []string{"call-123"}, want: "call-123", wantSeen: true},
		{name: "absent header attaches nothing", values: nil, wantSeen: false},
		{name: "empty value attaches nothing", values: []string{""}, wantSeen: false},
		// Same rule as the gRPC interceptor: multiple values are ambiguous
		// and refused rather than guessed between.
		{name: "multiple values are treated as absent", values: []string{"a", "b"}, wantSeen: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &okHandler{}
			mw := WithCallID(h)
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			for _, v := range tc.values {
				req.Header.Add(CallIDHeader, v)
			}
			w := httptest.NewRecorder()
			mw.ServeHTTP(w, req)
			if !h.called.Load() {
				t.Fatal("WithCallID must never reject on its own")
			}
			got, seen := h.gotCID.Load().(string)
			if seen != tc.wantSeen || (seen && got != tc.want) {
				t.Errorf("call id = (%q, %v), want (%q, %v)", got, seen, tc.want, tc.wantSeen)
			}
		})
	}
}

// TestRequireGenerationSlot_DrainsInFlightCalls is the DoD line "a
// generation rotation drains in-flight host-endpoint calls, proven by a
// test": a request holding a slot keeps the drain waiting, and the force
// cancel after the grace period reaches the handler through its request
// context. Synchronisation is on channels and the controller's own
// after-pause hook, never wall-clock polling; the one real wait is the
// drain grace, asserted only by ordering. No t.Parallel(): the hook seam
// forbids it (generation/testhooks.go).
func TestRequireGenerationSlot_DrainsInFlightCalls(t *testing.T) {
	c := generation.New()
	c.RegisterInstance("inst-d")

	entered := make(chan struct{})
	ctxCancelled := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done() // holds its slot until the drain force-cancels it
		close(ctxCancelled)
		w.WriteHeader(http.StatusOK)
	})
	mw := RequireGenerationSlot(handler, c)

	withIdentity := func(req *http.Request) *http.Request {
		ctx := context.WithValue(req.Context(), identityCtxKey{}, Identity{InstanceID: "inst-d"})
		return req.WithContext(ctx)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		w := httptest.NewRecorder()
		mw.ServeHTTP(w, withIdentity(httptest.NewRequest(http.MethodPost, "/", nil)))
	}()
	<-entered // the in-flight call holds its generation slot

	paused := make(chan struct{})
	c.SetAfterPauseHookForTest(func(string) { close(paused) })

	newReqResult := make(chan int, 1)
	go func() {
		<-paused
		// A NEW request arriving mid-drain, whose own context expires before
		// the drain completes: the ported behaviour is 503, not a hang and
		// not a pass-through to a paused generation.
		reqCtx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(reqCtx)
		w := httptest.NewRecorder()
		mw.ServeHTTP(w, withIdentity(req))
		newReqResult <- w.Code
	}()

	// Grace far below the test deadline: the in-flight handler only returns
	// when force-cancelled, so BeginDrain must report drained=false.
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelDrain()
	_, drained, err := c.BeginDrain(drainCtx, "inst-d", 200*time.Millisecond)
	if err != nil {
		t.Fatalf("BeginDrain: %v", err)
	}
	if drained {
		t.Error("BeginDrain reported a clean drain while a request held a slot — the middleware is not holding refcounts")
	}

	select {
	case <-ctxCancelled:
		// The force cancel reached the handler through its request context.
	case <-time.After(30 * time.Second):
		t.Fatal("in-flight handler was never force-cancelled by the drain")
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("in-flight request never completed after force cancel")
	}
	select {
	case code := <-newReqResult:
		if code != http.StatusServiceUnavailable {
			t.Errorf("mid-drain request with expired ctx: status = %d, want 503", code)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("mid-drain request never returned")
	}
}

func TestRequireGenerationSlot_PassThroughAndUnregistered(t *testing.T) {
	c := generation.New()

	t.Run("no identity in context passes through so the auth error is not masked", func(t *testing.T) {
		h := &okHandler{}
		mw := RequireGenerationSlot(h, c)
		w := httptest.NewRecorder()
		mw.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", nil))
		if !h.called.Load() {
			t.Fatal("pass-through did not reach the handler")
		}
	})

	t.Run("an unregistered instance is 503, not a pass-through", func(t *testing.T) {
		h := &okHandler{}
		mw := RequireGenerationSlot(h, c)
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		ctx := context.WithValue(req.Context(), identityCtxKey{}, Identity{InstanceID: "never-registered"})
		w := httptest.NewRecorder()
		mw.ServeHTTP(w, req.WithContext(ctx))
		if w.Code != http.StatusServiceUnavailable || h.called.Load() {
			t.Fatalf("status = %d, handler called = %v; want 503, false", w.Code, h.called.Load())
		}
	})
}

// TestChain pins the canonical order — token, then generation, then call-id
// — by asserting the observable consequence of each boundary: an auth
// failure never touches the generation controller, and a fully valid
// request reaches the handler with both identity and call id attached.
func TestChain(t *testing.T) {
	reg := identity.New()
	token, err := reg.Issue("inst-chain")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	c := generation.New()
	c.RegisterInstance("inst-chain")

	h := &okHandler{}
	chain := Chain(h, RegistryResolver{Registry: reg}, c)

	t.Run("bad token is rejected without consulting the generation gate", func(t *testing.T) {
		// The instance in the forged identity is deliberately NOT registered:
		// if the generation middleware ran first, this would 503; the 401
		// proves the token boundary comes first.
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Authorization", "Bearer forged")
		w := httptest.NewRecorder()
		chain.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", w.Code)
		}
	})

	t.Run("a valid request carries identity and call id into the handler", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(CallIDHeader, "call-42")
		w := httptest.NewRecorder()
		chain.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
		}
		id, _ := h.gotID.Load().(Identity)
		if id.InstanceID != "inst-chain" {
			t.Errorf("identity = %+v, want inst-chain", id)
		}
		if cid, _ := h.gotCID.Load().(string); cid != "call-42" {
			t.Errorf("call id = %q, want call-42", cid)
		}
	})
}
