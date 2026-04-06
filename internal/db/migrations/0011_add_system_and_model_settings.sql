CREATE TABLE IF NOT EXISTS system_settings (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS model_settings (
    provider    TEXT    NOT NULL,
    model_name  TEXT    NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    updated_at  TEXT    NOT NULL,
    PRIMARY KEY (provider, model_name)
);
