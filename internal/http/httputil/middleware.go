package httputil

import (
	"net/http"
	"strings"
)

// MaxRequestBodySize is the default body size limit (1 MiB) applied to all
// API and trigger endpoints.
const MaxRequestBodySize = 1 << 20

// cspHeader is the Content-Security-Policy value applied to every response.
//
// Design notes:
//   - 'unsafe-inline' in script-src: required for the theme-detection IIFE
//     embedded in frontend/index.html and for Vite dev-mode script injection.
//     A SHA-256 hash approach is impractical while Vite injects dynamic scripts.
//   - 'unsafe-inline' in style-src: Vite dev mode injects inline <style> tags;
//     CSS Modules in production may also emit inline style elements.
//   - data: in img-src: allows data-URI images used in SVG embedding patterns.
//   - connect-src 'self': SSE uses fetch to /api/v1/events (same origin only).
//   - frame-ancestors 'none': mirrors X-Frame-Options: DENY in CSP form; both
//     headers are set for defense in depth across older and newer browsers.
//   - Future external resources (CDN fonts, third-party scripts, WebSocket
//     endpoints) will require explicit CSP additions.
//   - HSTS is intentionally omitted: Gleipnir is a homelab tool expected to
//     run behind a TLS-terminating reverse proxy that handles HSTS itself.
const cspHeader = "default-src 'self'; " +
	"script-src 'self' 'unsafe-inline'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"font-src 'self'; " +
	"connect-src 'self'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'"

// SecurityHeaders is middleware that sets conservative HTTP security headers on
// every response. It must be registered before any other middleware so that
// headers are present even when a later middleware short-circuits the chain.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		w.Header().Set("Content-Security-Policy", cspHeader)
		next.ServeHTTP(w, r)
	})
}

// BodySizeLimit returns middleware that caps the request body at maxBytes.
// Uses http.MaxBytesReader so downstream Read/Decode calls receive an explicit
// "request body too large" error when the limit is exceeded.
func BodySizeLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// RequireJSON is middleware that rejects POST/PUT/PATCH requests whose
// Content-Type does not contain "application/json" with 415 Unsupported Media Type.
// GET, DELETE, HEAD, and OPTIONS requests are passed through unchanged.
func RequireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			ct := r.Header.Get("Content-Type")
			if !strings.Contains(ct, "application/json") {
				WriteError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json", "")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
