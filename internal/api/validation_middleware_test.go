package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rapp992/gleipnir/internal/api"
)

func TestBodySizeLimit(t *testing.T) {
	const limit = 10

	readBody := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	t.Run("body within limit passes through", func(t *testing.T) {
		handler := api.BodySizeLimit(limit)(readBody)
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("small"))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", w.Code)
		}
	})

	t.Run("body exceeding limit causes read error", func(t *testing.T) {
		handler := api.BodySizeLimit(limit)(readBody)
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", limit+1)))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})
}

func TestRequireJSON(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	cases := []struct {
		name        string
		method      string
		contentType string
		wantStatus  int
	}{
		{
			name:        "POST with application/json passes",
			method:      http.MethodPost,
			contentType: "application/json",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "POST with application/json; charset=utf-8 passes",
			method:      http.MethodPost,
			contentType: "application/json; charset=utf-8",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "POST with wrong content type returns 415",
			method:      http.MethodPost,
			contentType: "text/plain",
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "POST with missing content type returns 415",
			method:      http.MethodPost,
			contentType: "",
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "PUT with wrong content type returns 415",
			method:      http.MethodPut,
			contentType: "application/yaml",
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "PATCH with application/json passes",
			method:      http.MethodPatch,
			contentType: "application/json",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "GET passes without content type check",
			method:      http.MethodGet,
			contentType: "",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "DELETE passes without content type check",
			method:      http.MethodDelete,
			contentType: "",
			wantStatus:  http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := api.RequireJSON(ok)
			req := httptest.NewRequest(tc.method, "/", nil)
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tc.wantStatus)
			}
		})
	}
}
