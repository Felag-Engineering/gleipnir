// Package api provides shared HTTP response helpers and the /api/v1/ route tree.
// All API handlers should use WriteJSON and WriteError from this package to
// enforce the response envelope: {"data": T} for success, {"error": "...", "detail": "..."} for failure.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

type successEnvelope struct {
	Data any `json:"data"`
}

type errorEnvelope struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
}

// WriteJSON writes a JSON response body wrapped in {"data": data} with the
// given HTTP status code. Logs an error if encoding fails.
func WriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(successEnvelope{Data: data}); err != nil {
		slog.Error("failed to encode JSON response", "err", err)
	}
}

// WriteCreated writes a 201 Created response with a Location header and a
// {"data": data} JSON body.
func WriteCreated(w http.ResponseWriter, locationPath string, data any) {
	w.Header().Set("Location", locationPath)
	WriteJSON(w, http.StatusCreated, data)
}

// WriteError writes a JSON error response with {"error": msg, "detail": detail}
// and the given HTTP status code. detail is omitted from the response when empty.
func WriteError(w http.ResponseWriter, status int, msg, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(errorEnvelope{Error: msg, Detail: detail}); err != nil {
		slog.Error("failed to encode JSON error response", "err", err)
	}
}
