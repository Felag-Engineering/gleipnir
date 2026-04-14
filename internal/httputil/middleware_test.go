package httputil_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rapp992/gleipnir/internal/httputil"
)

func TestSecurityHeaders(t *testing.T) {
	// expectedHeaders maps each security header name to its expected value.
	// Changing any value in SecurityHeaders must be reflected here.
	expectedHeaders := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "strict-origin-when-cross-origin",
		"Permissions-Policy":      "geolocation=(), microphone=(), camera=()",
		"Content-Security-Policy": "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'",
	}

	t.Run("sets all security headers", func(t *testing.T) {
		handler := httputil.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		for name, want := range expectedHeaders {
			if got := w.Header().Get(name); got != want {
				t.Errorf("header %q = %q, want %q", name, got, want)
			}
		}
	})

	t.Run("does not overwrite downstream Content-Type", func(t *testing.T) {
		handler := httputil.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if got := w.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want %q", got, "application/json")
		}
	})

	t.Run("headers present on non-200 responses", func(t *testing.T) {
		handler := httputil.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))

		req := httptest.NewRequest(http.MethodGet, "/missing", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
		}

		for name, want := range expectedHeaders {
			if got := w.Header().Get(name); got != want {
				t.Errorf("header %q = %q, want %q", name, got, want)
			}
		}
	})
}
