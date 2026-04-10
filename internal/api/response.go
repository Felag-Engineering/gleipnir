// Package api provides the /api/v1/ route tree and convenience response helpers.
//
// WriteJSON, WriteError, and WriteCreated delegate to internal/httputil for the
// actual envelope encoding. They are re-exported here so that existing callers
// (admin, trigger, etc.) can continue to import "internal/api" without change.
package api

import (
	"net/http"

	"github.com/rapp992/gleipnir/internal/httputil"
)

// WriteJSON writes a JSON response body wrapped in {"data": data} with the
// given HTTP status code.
func WriteJSON(w http.ResponseWriter, status int, data any) {
	httputil.WriteJSON(w, status, data)
}

// WriteCreated writes a 201 Created response with a Location header and a
// {"data": data} JSON body.
func WriteCreated(w http.ResponseWriter, locationPath string, data any) {
	w.Header().Set("Location", locationPath)
	httputil.WriteJSON(w, http.StatusCreated, data)
}

// WriteError writes a JSON error response with {"error": msg, "detail": detail}
// and the given HTTP status code. detail is omitted from the response when empty.
func WriteError(w http.ResponseWriter, status int, msg, detail string) {
	httputil.WriteError(w, status, msg, detail)
}
