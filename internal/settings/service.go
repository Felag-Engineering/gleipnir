// Package settings provides read access to system-wide runtime settings stored
// in the system_settings table. It exists as a separate package so non-admin
// callers (trigger handlers, the agent launcher, the policy service) can depend
// on settings without importing the admin HTTP handler package.
package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// Querier is the narrow DB interface the Service needs. It is satisfied by
// *db.Queries directly, so callers can pass store.Queries() without any adapter.
type Querier interface {
	GetSystemSetting(ctx context.Context, key string) (db.SystemSetting, error)
}

// Service reads system-wide settings from the database.
type Service struct {
	q Querier
}

// NewService returns a Service backed by the given Querier.
func NewService(q Querier) *Service {
	return &Service{q: q}
}

// GetSystemDefault returns the provider and model name from the default_model
// system setting. When no default is configured (sql.ErrNoRows) it returns
// ("", "", nil) — the empty provider string is the caller-visible signal for
// "not configured". Other database errors are returned with %w wrapping.
func (s *Service) GetSystemDefault(ctx context.Context) (provider, modelName string, err error) {
	row, err := s.q.GetSystemSetting(ctx, "default_model")
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("read default_model: %w", err)
	}
	parts := strings.SplitN(row.Value, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid default_model format: %q", row.Value)
	}
	return parts[0], parts[1], nil
}

// GetPublicURL reads the public_url system setting and returns it.
// When the setting has not been configured (sql.ErrNoRows) it returns ("", nil).
// Other database errors are returned with %w wrapping.
func (s *Service) GetPublicURL(ctx context.Context) (string, error) {
	row, err := s.q.GetSystemSetting(ctx, "public_url")
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read public_url: %w", err)
	}
	return row.Value, nil
}
