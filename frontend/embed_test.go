package frontend

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestSPAHandler(t *testing.T) {
	mockFS := fstest.MapFS{
		"index.html":              {Data: []byte("<!DOCTYPE html><html></html>")},
		"assets/index-abc123.js":  {Data: []byte("console.log('app')")},
		"assets/index-abc123.css": {Data: []byte("body{}")},
		"favicon.ico":             {Data: []byte("icon-data")},
	}

	handler := newSPAHandler(mockFS)

	tests := []struct {
		name          string
		path          string
		wantStatus    int
		wantBody      string
		wantCacheCtrl string
	}{
		{
			name:          "root serves index.html",
			path:          "/",
			wantStatus:    http.StatusOK,
			wantBody:      "<!DOCTYPE html><html></html>",
			wantCacheCtrl: "no-cache",
		},
		{
			name:          "hashed asset served with immutable cache",
			path:          "/assets/index-abc123.js",
			wantStatus:    http.StatusOK,
			wantBody:      "console.log('app')",
			wantCacheCtrl: "public, max-age=31536000, immutable",
		},
		{
			name:          "unknown path falls back to index.html",
			path:          "/policies/123",
			wantStatus:    http.StatusOK,
			wantBody:      "<!DOCTYPE html><html></html>",
			wantCacheCtrl: "no-cache",
		},
		{
			name:          "favicon served directly",
			path:          "/favicon.ico",
			wantStatus:    http.StatusOK,
			wantBody:      "icon-data",
			wantCacheCtrl: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			resp := rec.Result()
			body, _ := io.ReadAll(resp.Body)

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if string(body) != tt.wantBody {
				t.Errorf("body = %q, want %q", string(body), tt.wantBody)
			}
			if tt.wantCacheCtrl != "" {
				got := resp.Header.Get("Cache-Control")
				if got != tt.wantCacheCtrl {
					t.Errorf("Cache-Control = %q, want %q", got, tt.wantCacheCtrl)
				}
			}
		})
	}
}
