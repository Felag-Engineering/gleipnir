package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rapp992/gleipnir/internal/api"
	"github.com/rapp992/gleipnir/internal/llm"
)

// stubModelLister implements api.ModelLister for tests.
type stubModelLister struct {
	models          map[string][]llm.ModelInfo
	invalidateCalls []string // provider names passed to InvalidateModelCache
	invalidateAll   int      // count of InvalidateAllModelCaches calls
	listErr         error    // if set, ListModels/ListAllModels return this
}

func (s *stubModelLister) ListModels(_ context.Context, provider string) ([]llm.ModelInfo, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	models, ok := s.models[provider]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q: cannot list models", provider)
	}
	return models, nil
}

func (s *stubModelLister) ListAllModels(_ context.Context) (map[string][]llm.ModelInfo, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.models, nil
}

func (s *stubModelLister) InvalidateModelCache(provider string) error {
	s.invalidateCalls = append(s.invalidateCalls, provider)
	if _, ok := s.models[provider]; !ok {
		return fmt.Errorf("unknown provider %q: cannot invalidate cache", provider)
	}
	return nil
}

func (s *stubModelLister) InvalidateAllModelCaches() {
	s.invalidateAll++
}

func newModelsRouter(lister api.ModelLister) http.Handler {
	r := chi.NewRouter()
	h := api.NewModelsHandler(lister, nil)
	r.Get("/models", h.List)
	r.Post("/models/refresh", h.Refresh)
	return r
}

func TestModelsHandler_List(t *testing.T) {
	lister := &stubModelLister{
		models: map[string][]llm.ModelInfo{
			"anthropic": {
				{Name: "claude-sonnet-4-6", DisplayName: "claude-sonnet-4-6"},
			},
			"google": {
				{Name: "gemini-2.0-flash", DisplayName: "Gemini 2.0 Flash"},
				{Name: "gemini-1.5-pro", DisplayName: "Gemini 1.5 Pro"},
			},
		},
	}

	t.Run("list all providers", func(t *testing.T) {
		srv := httptest.NewServer(newModelsRouter(lister))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/models")
		if err != nil {
			t.Fatalf("GET /models: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		var envelope struct {
			Data []struct {
				Provider string `json:"provider"`
				Models   []struct {
					Name        string `json:"name"`
					DisplayName string `json:"display_name"`
				} `json:"models"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode: %v", err)
		}

		if len(envelope.Data) != 2 {
			t.Fatalf("expected 2 providers, got %d", len(envelope.Data))
		}

		// Find total model count across providers.
		totalModels := 0
		for _, p := range envelope.Data {
			totalModels += len(p.Models)
		}
		if totalModels != 3 {
			t.Errorf("expected 3 total models, got %d", totalModels)
		}
	})

	t.Run("filter by provider", func(t *testing.T) {
		srv := httptest.NewServer(newModelsRouter(lister))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/models?provider=google")
		if err != nil {
			t.Fatalf("GET /models?provider=google: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		var envelope struct {
			Data []struct {
				Provider string `json:"provider"`
				Models   []struct {
					Name        string `json:"name"`
					DisplayName string `json:"display_name"`
				} `json:"models"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode: %v", err)
		}

		if len(envelope.Data) != 1 {
			t.Fatalf("expected 1 provider entry, got %d", len(envelope.Data))
		}
		if envelope.Data[0].Provider != "google" {
			t.Errorf("provider = %q, want %q", envelope.Data[0].Provider, "google")
		}
		if len(envelope.Data[0].Models) != 2 {
			t.Errorf("expected 2 google models, got %d", len(envelope.Data[0].Models))
		}
	})

	t.Run("unknown provider returns 400", func(t *testing.T) {
		srv := httptest.NewServer(newModelsRouter(lister))
		t.Cleanup(srv.Close)

		resp, err := http.Get(srv.URL + "/models?provider=openai")
		if err != nil {
			t.Fatalf("GET /models?provider=openai: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})
}

func TestModelsHandler_Refresh(t *testing.T) {
	t.Run("refresh all providers", func(t *testing.T) {
		lister := &stubModelLister{
			models: map[string][]llm.ModelInfo{
				"anthropic": {{Name: "claude-sonnet-4-6", DisplayName: "claude-sonnet-4-6"}},
				"google":    {{Name: "gemini-2.0-flash", DisplayName: "Gemini 2.0 Flash"}},
			},
		}
		srv := httptest.NewServer(newModelsRouter(lister))
		t.Cleanup(srv.Close)

		resp, err := http.Post(srv.URL+"/models/refresh", "", nil)
		if err != nil {
			t.Fatalf("POST /models/refresh: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		if lister.invalidateAll != 1 {
			t.Errorf("InvalidateAllModelCaches called %d times, want 1", lister.invalidateAll)
		}

		var envelope struct {
			Data []struct {
				Provider string `json:"provider"`
				Models   []struct {
					Name string `json:"name"`
				} `json:"models"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(envelope.Data) != 2 {
			t.Fatalf("expected 2 providers in response, got %d", len(envelope.Data))
		}
	})

	t.Run("refresh single provider", func(t *testing.T) {
		lister := &stubModelLister{
			models: map[string][]llm.ModelInfo{
				"anthropic": {{Name: "claude-sonnet-4-6", DisplayName: "claude-sonnet-4-6"}},
				"google":    {{Name: "gemini-2.0-flash", DisplayName: "Gemini 2.0 Flash"}},
			},
		}
		srv := httptest.NewServer(newModelsRouter(lister))
		t.Cleanup(srv.Close)

		resp, err := http.Post(srv.URL+"/models/refresh?provider=google", "", nil)
		if err != nil {
			t.Fatalf("POST /models/refresh?provider=google: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}

		if len(lister.invalidateCalls) != 1 || lister.invalidateCalls[0] != "google" {
			t.Errorf("InvalidateModelCache calls = %v, want [google]", lister.invalidateCalls)
		}
		if lister.invalidateAll != 0 {
			t.Errorf("InvalidateAllModelCaches should not have been called")
		}

		var envelope struct {
			Data []struct {
				Provider string `json:"provider"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(envelope.Data) != 1 || envelope.Data[0].Provider != "google" {
			t.Errorf("expected single google entry, got %+v", envelope.Data)
		}
	})

	t.Run("refresh unknown provider returns 400", func(t *testing.T) {
		lister := &stubModelLister{
			models: map[string][]llm.ModelInfo{},
		}
		srv := httptest.NewServer(newModelsRouter(lister))
		t.Cleanup(srv.Close)

		resp, err := http.Post(srv.URL+"/models/refresh?provider=openai", "", nil)
		if err != nil {
			t.Fatalf("POST /models/refresh?provider=openai: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})
}

func TestModelsHandler_ResponseShape(t *testing.T) {
	lister := &stubModelLister{
		models: map[string][]llm.ModelInfo{
			"google": {
				{Name: "gemini-2.0-flash", DisplayName: "Gemini 2.0 Flash"},
			},
		},
	}
	srv := httptest.NewServer(newModelsRouter(lister))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/models?provider=google")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Must have "data" key (response envelope).
	if _, ok := raw["data"]; !ok {
		t.Fatal("response missing \"data\" envelope key")
	}

	// Verify display_name uses snake_case.
	var envelope struct {
		Data []struct {
			Models []struct {
				DisplayName string `json:"display_name"`
			} `json:"models"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw["data"], &envelope.Data); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if envelope.Data[0].Models[0].DisplayName != "Gemini 2.0 Flash" {
		t.Errorf("display_name = %q, want %q", envelope.Data[0].Models[0].DisplayName, "Gemini 2.0 Flash")
	}
}
