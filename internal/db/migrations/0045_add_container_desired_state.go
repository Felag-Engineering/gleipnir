package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddContainerDesiredState creates the four container-substrate tables on
// existing deployments. New deployments get them from 0001_initial.sql; this
// migration is the upgrade path for existing databases.
//
// These tables ARE the desired state the reconciler converges toward (ADR-056,
// mcp-realignment-spec.md §7). The reconciler is level-triggered: each pass
// lists the real containers by label, diffs them against these rows, and takes
// one converging step. Nothing here tracks progress through an imperative
// sequence, because a crash mid-rotation is simply another observed state the
// next pass converges from.
//
//   - plugin_containers — one desired-state row per plugin instance.
//   - plugin_container_generations — rotation history, one row per generation,
//     each carrying its own instance token (start-new → health-gate → switch →
//     drain → stop). Tokens are stored as hex SHA-256 hashes, never raw, the
//     same posture the in-memory identity registry already takes.
//   - plugin_container_subnets — the per-instance /24 carved from the
//     configurable base pool. East-west isolation gives every instance its own
//     internal network, which makes subnets a finite resource that must never
//     be double-allocated; UNIQUE(pool_base, slot) is what enforces that, so
//     two racing allocators cannot both commit the same slot.
//   - plugin_container_images — accounting for loaded OCI images so GC can tell
//     which digests no generation still needs. Reference counts are DERIVED by
//     query rather than stored in a column: a counter maintained by hand drifts,
//     and a drifted counter here means either deleting an image still in use or
//     never reclaiming one.
//
// Schema and queries only — no reconciler, allocator, or GC consumes any of
// this yet. See schemas/sql_schemas.sql for the canonical column reference.
type AddContainerDesiredState struct{}

func (m *AddContainerDesiredState) Version() int { return 35 }
func (m *AddContainerDesiredState) Name() string {
	return "add_container_desired_state"
}

func (m *AddContainerDesiredState) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	// Probe the last table created by Up. If it exists, the other three do
	// too -- they ship as a single unit inside one transaction.
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='plugin_container_images'`,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check plugin_container_images existence: %w", err)
	}
	return count > 0, nil
}

func (m *AddContainerDesiredState) Up(ctx context.Context, tx *sql.Tx) error {
	ddl := `
CREATE TABLE plugin_containers (
    id                    TEXT    PRIMARY KEY,                                                    -- ULID
    plugin_instance_id    TEXT    NOT NULL UNIQUE REFERENCES plugin_instances(id) ON DELETE CASCADE,  -- one desired container per instance
    image_ref             TEXT    NOT NULL,                                                       -- repo:tag as loaded from the bundle
    image_digest          TEXT    NOT NULL,                                                       -- sha256:...; the pin that actually runs
    config_hash           TEXT    NOT NULL,                                                       -- hash of the effective config; a change is what makes a rotation necessary
    network_name          TEXT    NOT NULL,                                                       -- the instance's dedicated internal network
    memory_limit_bytes    INTEGER,                                                                -- nullable; NULL means no cgroup memory cap
    cpu_limit_millicores  INTEGER,                                                                -- nullable; NULL means no cgroup CPU cap
    desired_state         TEXT    NOT NULL CHECK(desired_state IN ('running', 'stopped')),        -- what the reconciler converges toward
    version               INTEGER NOT NULL DEFAULT 0,                                             -- ADR-038 CAS counter
    created_at            TEXT    NOT NULL,                                                       -- ISO 8601 UTC
    updated_at            TEXT    NOT NULL                                                        -- ISO 8601 UTC
);

CREATE TABLE plugin_container_generations (
    id                  TEXT    PRIMARY KEY,                                                      -- ULID
    plugin_instance_id  TEXT    NOT NULL REFERENCES plugin_instances(id) ON DELETE CASCADE,
    generation          INTEGER NOT NULL,                                                         -- monotonic per instance
    container_id        TEXT,                                                                     -- nullable: runtime-assigned, unknown until the container is created
    image_digest        TEXT    NOT NULL,                                                         -- what this generation actually runs, which may lag plugin_containers mid-rotation
    config_hash         TEXT    NOT NULL,
    token_hash          TEXT    NOT NULL UNIQUE,                                                  -- hex SHA-256 of the per-generation instance token; the raw token is never stored
    token_revoked_at    TEXT,                                                                     -- nullable, ISO 8601 UTC; set when the generation stops serving
    status              TEXT    NOT NULL CHECK(status IN (
                                    'pending',      -- row exists, container not yet created
                                    'starting',     -- container created, not yet health-gated
                                    'healthy',      -- passed the health gate, not yet switched to
                                    'active',       -- serving traffic
                                    'draining',     -- superseded, finishing in-flight work
                                    'stopped',      -- terminal, container gone
                                    'failed'        -- terminal, never reached healthy
                                )),
    status_detail       TEXT,                                                                     -- nullable; operator-facing explanation for failed/stopped
    created_at          TEXT    NOT NULL,                                                         -- ISO 8601 UTC
    updated_at          TEXT    NOT NULL,                                                         -- ISO 8601 UTC
    UNIQUE(plugin_instance_id, generation)
);
CREATE INDEX idx_plugin_container_generations_status ON plugin_container_generations(status);
CREATE INDEX idx_plugin_container_generations_image  ON plugin_container_generations(image_digest);

CREATE TABLE plugin_container_subnets (
    subnet              TEXT    PRIMARY KEY,                                                      -- rendered CIDR, e.g. 10.83.7.0/24
    plugin_instance_id  TEXT    NOT NULL UNIQUE REFERENCES plugin_instances(id) ON DELETE CASCADE,
    pool_base           TEXT    NOT NULL,                                                         -- the configured base pool this slot was carved from
    slot                INTEGER NOT NULL,                                                         -- index within pool_base; the allocator's unit of arithmetic
    allocated_at        TEXT    NOT NULL,                                                         -- ISO 8601 UTC
    UNIQUE(pool_base, slot)                                                                       -- the arbiter: two racing allocations of one slot cannot both commit
);

CREATE TABLE plugin_container_images (
    digest        TEXT    PRIMARY KEY,                                                            -- sha256:...
    reference     TEXT    NOT NULL,                                                               -- repo:tag the archive was loaded under
    plugin_id     TEXT    REFERENCES plugins(id) ON DELETE SET NULL,                              -- nullable so image accounting outlives an uninstall
    size_bytes    INTEGER,                                                                        -- nullable; reported by the runtime at load
    loaded_at     TEXT    NOT NULL,                                                               -- ISO 8601 UTC
    last_used_at  TEXT                                                                            -- nullable, ISO 8601 UTC; GC recency input
);`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create container substrate tables: %w", err)
	}

	slog.Info("migrated: created container substrate desired-state tables")
	return nil
}
