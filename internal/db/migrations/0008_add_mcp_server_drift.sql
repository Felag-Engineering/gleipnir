-- Migration: 0008
-- Adds has_drift flag to mcp_servers. Set to 1 when re-discovery finds tool
-- changes (added/removed/modified); cleared to 0 when re-discovery finds no changes.
ALTER TABLE mcp_servers ADD COLUMN has_drift INTEGER NOT NULL DEFAULT 0;

INSERT INTO schema_migrations(version, applied_at) VALUES (8, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));
