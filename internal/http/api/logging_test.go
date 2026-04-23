package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// captureLogger replaces slog.Default() with a handler that writes JSON to buf
// for the duration of the test and restores the original default on cleanup.
func captureLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	orig := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return &buf
}

// decodeLogLines parses newline-delimited JSON from buf into a slice of maps,
// one per log line, so tests can index fields by name.
func decodeLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	dec := json.NewDecoder(strings.NewReader(buf.String()))
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("decode log line: %v", err)
		}
		lines = append(lines, m)
	}
	return lines
}

// TestSlogContext_InjectsLoggerWithRequestFields verifies that slogContext
// injects a logger into the request context that carries request_id and
// remote_addr as structured fields.
func TestSlogContext_InjectsLoggerWithRequestFields(t *testing.T) {
	buf := captureLogger(t)

	var capturedLogger *slog.Logger
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedLogger = LoggerFromContext(r.Context())
		capturedLogger.InfoContext(r.Context(), "from handler")
		w.WriteHeader(http.StatusOK)
	})

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(slogContext)
	r.Get("/ping", inner)

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if capturedLogger == nil {
		t.Fatal("handler did not capture a logger from context")
	}

	lines := decodeLogLines(t, buf)
	if len(lines) == 0 {
		t.Fatal("no log lines emitted")
	}
	// The handler emitted exactly one line — pick it.
	var handlerLine map[string]any
	for _, l := range lines {
		if l["msg"] == "from handler" {
			handlerLine = l
			break
		}
	}
	if handlerLine == nil {
		t.Fatal("log line with msg='from handler' not found")
	}

	reqID, ok := handlerLine["request_id"].(string)
	if !ok || reqID == "" {
		t.Errorf("request_id missing or empty in log: %v", handlerLine)
	}
	remoteAddr, ok := handlerLine["remote_addr"].(string)
	if !ok || remoteAddr == "" {
		t.Errorf("remote_addr missing or empty in log: %v", handlerLine)
	}
}

// TestLoggerFromContext_BareContextReturnsDefault verifies that
// LoggerFromContext returns a usable non-nil logger even when the context
// was never enriched by slogContext middleware.
func TestLoggerFromContext_BareContextReturnsDefault(t *testing.T) {
	lg := LoggerFromContext(context.Background())
	if lg == nil {
		t.Fatal("expected non-nil logger, got nil")
	}
	// Must not panic.
	lg.InfoContext(context.Background(), "bare context test")
}

// TestSlogAccess_EmitsStructuredAccessLog verifies the JSON shape of the access
// log emitted by slogAccess for a normal request.
func TestSlogAccess_EmitsStructuredAccessLog(t *testing.T) {
	buf := captureLogger(t)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(slogContext)
	r.Use(slogAccess)
	r.Get("/thing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/thing", nil)
	req.RemoteAddr = "10.0.0.1:9999"
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	lines := decodeLogLines(t, buf)
	var accessLine map[string]any
	for _, l := range lines {
		if l["msg"] == "http request" {
			accessLine = l
			break
		}
	}
	if accessLine == nil {
		t.Fatal("access log line with msg='http request' not found")
	}

	if accessLine["method"] != "GET" {
		t.Errorf("method = %v, want GET", accessLine["method"])
	}
	if accessLine["path"] != "/thing" {
		t.Errorf("path = %v, want /thing", accessLine["path"])
	}
	if status, ok := accessLine["status"].(float64); !ok || int(status) != http.StatusNoContent {
		t.Errorf("status = %v, want %d", accessLine["status"], http.StatusNoContent)
	}
	durMS, ok := accessLine["duration_ms"].(float64)
	if !ok {
		t.Errorf("duration_ms missing or wrong type: %v", accessLine["duration_ms"])
	} else if durMS < 0 {
		t.Errorf("duration_ms = %v, want >= 0", durMS)
	}
	if reqID, ok := accessLine["request_id"].(string); !ok || reqID == "" {
		t.Errorf("request_id missing or empty in access log: %v", accessLine)
	}
	if remoteAddr, ok := accessLine["remote_addr"].(string); !ok || remoteAddr == "" {
		t.Errorf("remote_addr missing or empty in access log: %v", accessLine)
	}
}

// TestSlogAccess_CapturesStatusCode verifies that slogAccess logs the correct
// HTTP status code for handlers that call WriteHeader with different codes.
func TestSlogAccess_CapturesStatusCode(t *testing.T) {
	cases := []struct {
		name string
		code int
	}{
		{"200", http.StatusOK},
		{"404", http.StatusNotFound},
		{"500", http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLogger(t)

			code := tc.code
			r := chi.NewRouter()
			r.Use(middleware.RequestID)
			r.Use(middleware.RealIP)
			r.Use(slogContext)
			r.Use(slogAccess)
			r.Get("/check", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			})

			req := httptest.NewRequest(http.MethodGet, "/check", nil)
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)

			lines := decodeLogLines(t, buf)
			var accessLine map[string]any
			for _, l := range lines {
				if l["msg"] == "http request" {
					accessLine = l
					break
				}
			}
			if accessLine == nil {
				t.Fatal("access log line not found")
			}

			got, ok := accessLine["status"].(float64)
			if !ok {
				t.Fatalf("status field wrong type: %v", accessLine["status"])
			}
			if int(got) != tc.code {
				t.Errorf("status = %d, want %d", int(got), tc.code)
			}
		})
	}
}

// TestSlogAccess_UsesRawPathNotRoutePattern verifies that the access log
// records the actual request path, not the chi route pattern. The route pattern
// is a metrics concern; logs should show the real path for debugging.
func TestSlogAccess_UsesRawPathNotRoutePattern(t *testing.T) {
	buf := captureLogger(t)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(slogContext)
	r.Use(slogAccess)
	r.Get("/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/items/abc123", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	lines := decodeLogLines(t, buf)
	var accessLine map[string]any
	for _, l := range lines {
		if l["msg"] == "http request" {
			accessLine = l
			break
		}
	}
	if accessLine == nil {
		t.Fatal("access log line not found")
	}

	if accessLine["path"] != "/items/abc123" {
		t.Errorf("path = %v, want /items/abc123 (raw path, not route pattern)", accessLine["path"])
	}
}
