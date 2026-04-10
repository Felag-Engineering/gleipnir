package httputil

import (
	"net/http"
	"strings"
)

// MaxRequestBodySize is the default body size limit (1 MiB) applied to all
// API and trigger endpoints.
const MaxRequestBodySize = 1 << 20

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
