package trigger

import (
	"context"
	"database/sql"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/settings"
)

// stubSettingsQuerier is a minimal settings.Querier for trigger package tests.
type stubSettingsQuerier struct {
	settings map[string]db.SystemSetting
}

func (q *stubSettingsQuerier) GetSystemSetting(_ context.Context, key string) (db.SystemSetting, error) {
	row, ok := q.settings[key]
	if !ok {
		return db.SystemSetting{}, sql.ErrNoRows
	}
	return row, nil
}

// newTestSettings builds a *settings.Service that returns the given provider and
// model name from GetSystemDefault. Pass ("", "") to simulate "no default configured".
func newTestSettings(provider, modelName string) *settings.Service {
	q := &stubSettingsQuerier{settings: make(map[string]db.SystemSetting)}
	if provider != "" || modelName != "" {
		q.settings["default_model"] = db.SystemSetting{
			Key:   "default_model",
			Value: provider + ":" + modelName,
		}
	}
	return settings.NewService(q)
}
