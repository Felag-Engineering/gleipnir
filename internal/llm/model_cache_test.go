package llm_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/llm"
)

func TestModelCache_LoadOnce_CallsFetchOnlyOnce(t *testing.T) {
	mc := llm.NewModelCache("TestProvider")
	callCount := 0

	fetch := func() (map[string]string, error) {
		callCount++
		return map[string]string{"model-a": "Model A"}, nil
	}

	mc.LoadOnce(fetch)
	mc.LoadOnce(fetch)

	if callCount != 1 {
		t.Errorf("fetch called %d times, want 1", callCount)
	}
}

func TestModelCache_LoadOnce_StoresError(t *testing.T) {
	mc := llm.NewModelCache("TestProvider")
	fetchErr := errors.New("API unavailable")
	callCount := 0

	fetch := func() (map[string]string, error) {
		callCount++
		return nil, fetchErr
	}

	mc.LoadOnce(fetch)
	// A second call must not re-invoke fetch even after an error.
	mc.LoadOnce(fetch)

	if callCount != 1 {
		t.Errorf("fetch called %d times after error, want 1", callCount)
	}

	err := mc.ValidateModelName("any-model")
	if err == nil {
		t.Fatal("expected error from ValidateModelName after failed fetch, got nil")
	}
	if !strings.Contains(err.Error(), "could not validate model name") {
		t.Errorf("error message %q should contain 'could not validate model name'", err.Error())
	}
}

func TestModelCache_ValidateModelName(t *testing.T) {
	mc := llm.NewModelCache("Acme")
	mc.LoadOnce(func() (map[string]string, error) {
		return map[string]string{
			"acme-fast":  "Acme Fast",
			"acme-smart": "Acme Smart",
		}, nil
	})

	t.Run("known_model_returns_nil", func(t *testing.T) {
		if err := mc.ValidateModelName("acme-fast"); err != nil {
			t.Errorf("expected nil error for known model, got %v", err)
		}
	})

	t.Run("unknown_model_returns_descriptive_error", func(t *testing.T) {
		err := mc.ValidateModelName("acme-unknown")
		if err == nil {
			t.Fatal("expected error for unknown model, got nil")
		}
		msg := err.Error()
		if !strings.Contains(msg, "Acme") {
			t.Errorf("error %q should contain provider name 'Acme'", msg)
		}
		if !strings.Contains(msg, "acme-unknown") {
			t.Errorf("error %q should contain the unknown model name", msg)
		}
		if !strings.Contains(msg, "acme-fast") {
			t.Errorf("error %q should list known models", msg)
		}
	})

	t.Run("cache_error_returns_wrapped_error", func(t *testing.T) {
		mcErr := llm.NewModelCache("Acme")
		fetchErr := errors.New("connection refused")
		mcErr.LoadOnce(func() (map[string]string, error) {
			return nil, fetchErr
		})

		err := mcErr.ValidateModelName("any")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, fetchErr) {
			t.Errorf("expected wrapped fetchErr, got %v", err)
		}
	})
}

func TestModelCache_ListModels(t *testing.T) {
	t.Run("returns_sorted_model_info", func(t *testing.T) {
		mc := llm.NewModelCache("TestProvider")
		mc.LoadOnce(func() (map[string]string, error) {
			return map[string]string{
				"model-z": "Model Z",
				"model-a": "Model A",
				"model-m": "Model M",
			}, nil
		})

		models, err := mc.ListModels()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(models) != 3 {
			t.Fatalf("expected 3 models, got %d", len(models))
		}
		// Must be sorted by name.
		if models[0].Name != "model-a" || models[1].Name != "model-m" || models[2].Name != "model-z" {
			t.Errorf("unexpected order: %v", models)
		}
		if models[0].DisplayName != "Model A" {
			t.Errorf("DisplayName = %q, want %q", models[0].DisplayName, "Model A")
		}
	})

	t.Run("returns_error_when_cache_failed", func(t *testing.T) {
		mc := llm.NewModelCache("TestProvider")
		fetchErr := errors.New("timeout")
		mc.LoadOnce(func() (map[string]string, error) {
			return nil, fetchErr
		})

		_, err := mc.ListModels()
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, fetchErr) {
			t.Errorf("expected fetchErr, got %v", err)
		}
	})
}

func TestModelCache_Invalidate_AllowsRefetch(t *testing.T) {
	mc := llm.NewModelCache("TestProvider")
	callCount := 0

	fetch := func() (map[string]string, error) {
		callCount++
		return map[string]string{"model-a": "Model A"}, nil
	}

	mc.LoadOnce(fetch)
	if callCount != 1 {
		t.Fatalf("expected 1 fetch call before invalidate, got %d", callCount)
	}

	mc.Invalidate()
	mc.LoadOnce(fetch)

	if callCount != 2 {
		t.Errorf("expected 2 fetch calls after invalidate, got %d", callCount)
	}
}
