package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
)

// StartHTTPGate starts the gate server behind an HTTP listener on a random
// localhost port. It returns the URL to include in --mcp-config and a shutdown
// function that closes the listener.
//
// Claude Code cannot connect to an in-process stdio server, so the gate runs
// as an HTTP MCP server on 127.0.0.1 and is referenced by URL in the
// --mcp-config JSON. gate_http.go and gate.go share the same package so this
// file can call gate.dispatch directly.
func StartHTTPGate(ctx context.Context, gate *GateServer) (url string, shutdown func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("listen on localhost: %w", err)
	}

	port := ln.Addr().(*net.TCPAddr).Port
	gateURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}

		var req jsonrpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			slog.WarnContext(r.Context(), "gate_http: malformed JSON-RPC request", "err", err)
			http.Error(w, "malformed JSON-RPC request", http.StatusBadRequest)
			return
		}

		// Per JSON-RPC 2.0: notifications have a null or absent ID.
		// The server must not respond to notifications.
		isNotification := req.ID == nil || string(req.ID) == "null"
		if isNotification {
			w.WriteHeader(http.StatusOK)
			return
		}

		resp := gate.dispatch(ctx, req)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			slog.WarnContext(r.Context(), "gate_http: failed to write response", "err", err)
		}
	})

	srv := &http.Server{Handler: handler}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.WarnContext(ctx, "gate_http: server error", "err", err)
		}
	}()

	shutdownFn := func() {
		if err := srv.Shutdown(context.Background()); err != nil {
			slog.Warn("gate_http: shutdown error", "err", err)
		}
	}

	return gateURL, shutdownFn, nil
}
