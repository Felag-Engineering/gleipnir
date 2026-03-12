-- Migration: 0003
-- Adds system_prompt column to runs table.
-- Persists the rendered system prompt at run start for display in the run detail UI.

BEGIN;

ALTER TABLE runs ADD COLUMN system_prompt TEXT;

INSERT INTO schema_migrations (version, applied_at)
VALUES (3, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));

COMMIT;
