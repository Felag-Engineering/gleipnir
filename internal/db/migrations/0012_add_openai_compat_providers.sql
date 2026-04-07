CREATE TABLE openai_compat_providers (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT    NOT NULL UNIQUE,
    base_url          TEXT    NOT NULL,
    api_key_encrypted TEXT    NOT NULL,
    created_at        TEXT    NOT NULL,
    updated_at        TEXT    NOT NULL
);

CREATE INDEX idx_openai_compat_providers_name ON openai_compat_providers(name);
