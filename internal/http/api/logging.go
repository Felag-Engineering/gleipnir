package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// loggerContextKey is the private key type used to store the per-request
// slog.Logger in the request context. Using a private type prevents collisions
// with other packages that might store values under the same string key.
type loggerContextKey struct{}

// withLogger returns a new context carrying lg as the per-request logger.
func withLogger(ctx context.Context, lg *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerContextKey{}, lg)
}

// LoggerFromContext returns the request-scoped logger injected by slogContext.
// If the context has no logger — for example in tests that bypass middleware —
// slog.Default() is returned so callers can always log safely without a nil check.
func LoggerFromContext(ctx context.Context) *slog.Logger {
	if lg, ok := ctx.Value(loggerContextKey{}).(*slog.Logger); ok && lg != nil {
		return lg
	}
	return slog.Default()
}

// slogContext is middleware that enriches the request context with a structured
// logger carrying request_id and remote_addr. It must run after middleware.RequestID
// and middleware.RealIP in the chain so those values are available.
//
// It does not emit any log line — that responsibility belongs to slogAccess.
func slogContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := middleware.GetReqID(r.Context())
		// middleware.RealIP mutates r.RemoteAddr directly — no separate context value.
		remoteAddr := r.RemoteAddr

		lg := slog.Default().With(
			"request_id", reqID,
			"remote_addr", remoteAddr,
		)

		next.ServeHTTP(w, r.WithContext(withLogger(r.Context(), lg)))
	})
}

// slogAccess is middleware that emits a structured JSON access log line after
// each response is complete. It must run after slogContext so the logger it
// retrieves already carries request_id and remote_addr.
func slogAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()

		next.ServeHTTP(ww, r)

		durationMS := float64(time.Since(start).Microseconds()) / 1000.0

		status := ww.Status()
		// net/http treats a handler that never calls WriteHeader as an implicit 200.
		if status == 0 {
			status = http.StatusOK
		}

		lg := LoggerFromContext(r.Context())
		lg.LogAttrs(r.Context(), slog.LevelInfo, "http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", status),
			slog.Float64("duration_ms", durationMS),
		)
	})
}
