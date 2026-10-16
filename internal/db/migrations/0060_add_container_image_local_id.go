package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddContainerImageLocalID adds plugin_container_images.local_image_id: the
// image ID the container runtime reported for the loaded image when the
// installer verified it against the signed archive. The pin (digest) is the
// image's config digest, but a runtime backed by the containerd image store
// names its images by manifest digest, so neither the bare config digest nor
// repo@config-digest resolves there. Containers are created from this ID.
// NULL means the row predates the column.
type AddContainerImageLocalID struct{}

func (m *AddContainerImageLocalID) Version() int { return 50 }
func (m *AddContainerImageLocalID) Name() string { return "add_container_image_local_id" }

func (m *AddContainerImageLocalID) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	return tableHasColumn(ctx, db, "plugin_container_images", "local_image_id")
}

func (m *AddContainerImageLocalID) Up(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		`ALTER TABLE plugin_container_images ADD COLUMN local_image_id TEXT`,
	); err != nil {
		return fmt.Errorf("add plugin_container_images.local_image_id: %w", err)
	}
	slog.Info("migrated: added local_image_id to plugin_container_images")
	return nil
}
