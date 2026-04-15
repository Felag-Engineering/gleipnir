package admin

import (
	"context"

	"github.com/rapp992/gleipnir/internal/db"
)

// QuerierAdapter wraps *db.Queries to satisfy the AdminQuerier interface.
// The adapter exists because AdminQuerier uses flat parameter lists for upsert
// methods, whereas db.Queries uses param structs — so a direct assignment is
// not possible even though the return types are now shared db types.
type QuerierAdapter struct {
	q *db.Queries
}

func NewQuerierAdapter(q *db.Queries) *QuerierAdapter {
	return &QuerierAdapter{q: q}
}

func (a *QuerierAdapter) GetSystemSetting(ctx context.Context, key string) (db.SystemSetting, error) {
	return a.q.GetSystemSetting(ctx, key)
}

func (a *QuerierAdapter) UpsertSystemSetting(ctx context.Context, key, value, updatedAt string) error {
	return a.q.UpsertSystemSetting(ctx, db.UpsertSystemSettingParams{
		Key:       key,
		Value:     value,
		UpdatedAt: updatedAt,
	})
}

func (a *QuerierAdapter) DeleteSystemSetting(ctx context.Context, key string) error {
	return a.q.DeleteSystemSetting(ctx, key)
}

func (a *QuerierAdapter) ListSystemSettings(ctx context.Context) ([]db.SystemSetting, error) {
	return a.q.ListSystemSettings(ctx)
}

func (a *QuerierAdapter) ListEnabledModels(ctx context.Context) ([]db.ListEnabledModelsRow, error) {
	return a.q.ListEnabledModels(ctx)
}

func (a *QuerierAdapter) UpsertModelSetting(ctx context.Context, provider, modelName string, enabled int64, updatedAt string) error {
	return a.q.UpsertModelSetting(ctx, db.UpsertModelSettingParams{
		Provider:  provider,
		ModelName: modelName,
		Enabled:   enabled,
		UpdatedAt: updatedAt,
	})
}

func (a *QuerierAdapter) ListModelSettings(ctx context.Context) ([]db.ModelSetting, error) {
	return a.q.ListModelSettings(ctx)
}
