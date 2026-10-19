package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddInstanceResourceOverride adds the per-instance admin override of a v2
// plugin's declared resource limits (issue #1000).
//
// Both columns are nullable and a NULL means "the admin said nothing", which
// is distinct from "no limit": resources.Resolve falls through to the
// manifest value and then the host default, per field. Bounds are enforced at
// the API layer through resources.FromManifestMiB, not with CHECK constraints,
// so the validation lives in one place.
type AddInstanceResourceOverride struct{}

func (m *AddInstanceResourceOverride) Version() int { return 52 }
func (m *AddInstanceResourceOverride) Name() string { return "add_instance_resource_override" }

func (m *AddInstanceResourceOverride) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('plugin_instances')
		 WHERE name IN ('resource_memory_mb_override', 'resource_cpu_millicores_override')`,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("inspect plugin_instances columns: %w", err)
	}
	return n == 2, nil
}

func (m *AddInstanceResourceOverride) Up(ctx context.Context, tx *sql.Tx) error {
	for _, ddl := range []string{
		`ALTER TABLE plugin_instances ADD COLUMN resource_memory_mb_override INTEGER`,
		`ALTER TABLE plugin_instances ADD COLUMN resource_cpu_millicores_override INTEGER`,
	} {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("add resource override column: %w", err)
		}
	}
	slog.Info("migrated: added resource override columns to plugin_instances")
	return nil
}
