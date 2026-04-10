package httputil_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rapp992/gleipnir/internal/httputil"
)

func TestWriteJSON(t *testing.T) {
	t.Run("wraps data in success envelope", func(t *testing.T) {
		w := httptest.NewRecorder()
		httputil.WriteJSON(w, http.StatusOK, map[string]string{"key": "value"})

		var got map[string]any
		if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
			t.Fatalf("decode response: %v", err)
		}

		data, ok := got["data"]
		if !ok {
			t.Fatal("expected 'data' key in response")
		}
		dataMap, ok := data.(map[string]any)
		if !ok {
			t.Fatalf("expected data to be an object, got %T", data)
		}
		if dataMap["key"] != "value" {
			t.Errorf("got data.key=%q, want %q", dataMap["key"], "value")
		}
	})

	t.Run("sets Content-Type to application/json", func(t *testing.T) {
		w := httptest.NewRecorder()
		httputil.WriteJSON(w, http.StatusOK, nil)

		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("got Content-Type=%q, want %q", ct, "application/json")
		}
	})

	t.Run("uses the provided HTTP status code", func(t *testing.T) {
		cases := []int{http.StatusOK, http.StatusCreated, http.StatusAccepted}
		for _, status := range cases {
			w := httptest.NewRecorder()
			httputil.WriteJSON(w, status, nil)
			if w.Code != status {
				t.Errorf("status %d: got code %d, want %d", status, w.Code, status)
			}
		}
	})

	t.Run("encodes nil data as null inside envelope", func(t *testing.T) {
		w := httptest.NewRecorder()
		httputil.WriteJSON(w, http.StatusOK, nil)

		var got map[string]any
		if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if _, ok := got["data"]; !ok {
			t.Fatal("expected 'data' key in response")
		}
		if got["data"] != nil {
			t.Errorf("got data=%v, want nil", got["data"])
		}
	})
}

func TestWriteError(t *testing.T) {
	t.Run("includes error and detail when detail is non-empty", func(t *testing.T) {
		w := httptest.NewRecorder()
		httputil.WriteError(w, http.StatusBadRequest, "bad input", "field X is required")

		var got map[string]any
		if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if got["error"] != "bad input" {
			t.Errorf("got error=%q, want %q", got["error"], "bad input")
		}
		if got["detail"] != "field X is required" {
			t.Errorf("got detail=%q, want %q", got["detail"], "field X is required")
		}
	})

	t.Run("omits detail key when detail is empty string", func(t *testing.T) {
		w := httptest.NewRecorder()
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")

		var got map[string]any
		if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if _, present := got["detail"]; present {
			t.Error("expected 'detail' key to be absent when detail is empty")
		}
	})

	t.Run("sets Content-Type to application/json", func(t *testing.T) {
		w := httptest.NewRecorder()
		httputil.WriteError(w, http.StatusBadRequest, "err", "")

		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("got Content-Type=%q, want %q", ct, "application/json")
		}
	})

	t.Run("uses the provided HTTP status code", func(t *testing.T) {
		cases := []int{http.StatusBadRequest, http.StatusNotFound, http.StatusInternalServerError}
		for _, status := range cases {
			w := httptest.NewRecorder()
			httputil.WriteError(w, status, "error", "")
			if w.Code != status {
				t.Errorf("status %d: got code %d, want %d", status, w.Code, status)
			}
		}
	})
}
