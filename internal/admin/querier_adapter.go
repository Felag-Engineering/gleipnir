package admin

import (
	"context"

	"github.com/rapp992/gleipnir/internal/db"
)

// QuerierAdapter wraps *db.Queries to satisfy the AdminQuerier interface.
type QuerierAdapter struct {
	q *db.Queries
}

func NewQuerierAdapter(q *db.Queries) *QuerierAdapter {
	return &QuerierAdapter{q: q}
}

func (a *QuerierAdapter) GetSystemSetting(ctx context.Context, key string) (SystemSettingRow, error) {
	row, err := a.q.GetSystemSetting(ctx, key)
	if err != nil {
		return SystemSettingRow{}, err
	}
	return SystemSettingRow{
		Key:       row.Key,
		Value:     row.Value,
		UpdatedAt: row.UpdatedAt,
	}, nil
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

func (a *QuerierAdapter) ListSystemSettings(ctx context.Context) ([]SystemSettingRow, error) {
	rows, err := a.q.ListSystemSettings(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]SystemSettingRow, len(rows))
	for i, row := range rows {
		result[i] = SystemSettingRow{
			Key:       row.Key,
			Value:     row.Value,
			UpdatedAt: row.UpdatedAt,
		}
	}
	return result, nil
}

func (a *QuerierAdapter) ListDisabledModels(ctx context.Context) ([]DisabledModelRow, error) {
	rows, err := a.q.ListDisabledModels(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]DisabledModelRow, len(rows))
	for i, row := range rows {
		result[i] = DisabledModelRow{
			Provider:  row.Provider,
			ModelName: row.ModelName,
		}
	}
	return result, nil
}

func (a *QuerierAdapter) UpsertModelSetting(ctx context.Context, provider, modelName string, enabled int64, updatedAt string) error {
	return a.q.UpsertModelSetting(ctx, db.UpsertModelSettingParams{
		Provider:  provider,
		ModelName: modelName,
		Enabled:   enabled,
		UpdatedAt: updatedAt,
	})
}

func (a *QuerierAdapter) ListModelSettings(ctx context.Context) ([]ModelSettingRow, error) {
	rows, err := a.q.ListModelSettings(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]ModelSettingRow, len(rows))
	for i, row := range rows {
		result[i] = ModelSettingRow{
			Provider:  row.Provider,
			ModelName: row.ModelName,
			Enabled:   row.Enabled,
			UpdatedAt: row.UpdatedAt,
		}
	}
	return result, nil
}
