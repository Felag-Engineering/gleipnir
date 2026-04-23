// Package httputil provides shared HTTP response helpers for writing
// JSON API responses with a consistent envelope format.
//
// All handler packages (api, auth, admin, trigger) should use these helpers
// to enforce the standard response envelope:
//
//	{"data": T}                          for success
//	{"error": "...", "detail": "..."}    for failure
//	{"error": "...", "detail": "...", "issues": [...]} for validation failures
package httputil

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

type successEnvelope struct {
	Data any `json:"data"`
}

type errorEnvelope struct {
	Error  string       `json:"error"`
	Detail string       `json:"detail,omitempty"`
	Issues []ErrorIssue `json:"issues,omitempty"`
}

// ErrorIssue is a structured validation failure with an optional field path.
// It is included in the API error envelope when validation fails so clients
// can map errors back to specific form fields. Field is omitted when the error
// is cross-cutting (not specific to a single input).
type ErrorIssue struct {
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
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

// WriteError writes a JSON error response with {"error": msg, "detail": detail}
// and the given HTTP status code. detail is omitted from the response when empty.
// Use WriteValidationError when structured field issues are available.
func WriteError(w http.ResponseWriter, status int, msg, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(errorEnvelope{Error: msg, Detail: detail}); err != nil {
		slog.Error("failed to encode JSON error response", "err", err)
	}
}

// WriteValidationError writes a structured validation error response that
// includes both the legacy detail string (for old clients) and a typed issues
// array (for new clients that surface per-field errors). The issues slice is
// omitted from the JSON when nil or empty.
func WriteValidationError(w http.ResponseWriter, status int, msg, detail string, issues []ErrorIssue) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	env := errorEnvelope{Error: msg, Detail: detail, Issues: issues}
	if err := json.NewEncoder(w).Encode(env); err != nil {
		slog.Error("failed to encode JSON validation error response", "err", err)
	}
}

// WriteCreated writes a 201 Created response with a Location header and a
// {"data": data} JSON body.
func WriteCreated(w http.ResponseWriter, locationPath string, data any) {
	w.Header().Set("Location", locationPath)
	WriteJSON(w, http.StatusCreated, data)
}
