package settings

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// stubQuerier is an in-memory Querier for settings tests.
type stubQuerier struct {
	settings map[string]db.SystemSetting
	err      error // if non-nil, returned by every GetSystemSetting call
}

func (s *stubQuerier) GetSystemSetting(_ context.Context, key string) (db.SystemSetting, error) {
	if s.err != nil {
		return db.SystemSetting{}, s.err
	}
	row, ok := s.settings[key]
	if !ok {
		return db.SystemSetting{}, sql.ErrNoRows
	}
	return row, nil
}

func TestGetSystemDefault(t *testing.T) {
	t.Run("well-formed value is parsed", func(t *testing.T) {
		q := &stubQuerier{settings: map[string]db.SystemSetting{
			"default_model": {Key: "default_model", Value: "anthropic:claude-sonnet-4-6"},
		}}
		svc := NewService(q)

		provider, modelName, err := svc.GetSystemDefault(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if provider != "anthropic" {
			t.Errorf("provider = %q, want %q", provider, "anthropic")
		}
		if modelName != "claude-sonnet-4-6" {
			t.Errorf("modelName = %q, want %q", modelName, "claude-sonnet-4-6")
		}
	})

	t.Run("unset returns empty strings and nil error", func(t *testing.T) {
		q := &stubQuerier{settings: map[string]db.SystemSetting{}}
		svc := NewService(q)

		provider, modelName, err := svc.GetSystemDefault(context.Background())
		if err != nil {
			t.Fatalf("expected nil error when default_model not set, got: %v", err)
		}
		if provider != "" || modelName != "" {
			t.Errorf("expected empty strings, got provider=%q modelName=%q", provider, modelName)
		}
	})

	t.Run("malformed value returns error", func(t *testing.T) {
		q := &stubQuerier{settings: map[string]db.SystemSetting{
			"default_model": {Key: "default_model", Value: "no-colon-here"},
		}}
		svc := NewService(q)

		_, _, err := svc.GetSystemDefault(context.Background())
		if err == nil {
			t.Fatal("expected error for malformed default_model, got nil")
		}
	})

	t.Run("arbitrary querier error is wrapped and propagated", func(t *testing.T) {
		sentinel := errors.New("db unavailable")
		q := &stubQuerier{err: sentinel}
		svc := NewService(q)

		_, _, err := svc.GetSystemDefault(context.Background())
		if !errors.Is(err, sentinel) {
			t.Errorf("expected error wrapping %v, got %v", sentinel, err)
		}
	})
}

func TestGetPublicURL(t *testing.T) {
	t.Run("stored value is returned verbatim", func(t *testing.T) {
		q := &stubQuerier{settings: map[string]db.SystemSetting{
			"public_url": {Key: "public_url", Value: "https://gleipnir.example.com"},
		}}
		svc := NewService(q)

		got, err := svc.GetPublicURL(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://gleipnir.example.com" {
			t.Errorf("got %q, want %q", got, "https://gleipnir.example.com")
		}
	})

	t.Run("unset returns empty string and nil error", func(t *testing.T) {
		q := &stubQuerier{settings: map[string]db.SystemSetting{}}
		svc := NewService(q)

		got, err := svc.GetPublicURL(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("arbitrary querier error is wrapped and propagated", func(t *testing.T) {
		sentinel := errors.New("db unavailable")
		q := &stubQuerier{err: sentinel}
		svc := NewService(q)

		_, err := svc.GetPublicURL(context.Background())
		if !errors.Is(err, sentinel) {
			t.Errorf("expected error wrapping %v, got %v", sentinel, err)
		}
	})
}
