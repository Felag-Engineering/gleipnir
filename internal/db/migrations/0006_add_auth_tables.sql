-- Migration: 0006
-- Adds users and sessions tables for authentication.
--
-- users.deactivated_at is nullable — a non-null value means the account
-- has been soft-deleted and must not be used for login.
--
-- sessions.token is a random opaque value stored in a cookie. The index
-- on token is the hot path for every authenticated request.

CREATE TABLE users (
    id              TEXT    PRIMARY KEY,  -- ULID
    username        TEXT    NOT NULL UNIQUE,
    password_hash   TEXT    NOT NULL,
    created_at      TEXT    NOT NULL,     -- ISO 8601 UTC
    deactivated_at  TEXT                  -- nullable, ISO 8601 UTC
);

CREATE TABLE sessions (
    id          TEXT    PRIMARY KEY,  -- ULID
    user_id     TEXT    NOT NULL REFERENCES users(id),
    token       TEXT    NOT NULL UNIQUE,
    created_at  TEXT    NOT NULL,     -- ISO 8601 UTC
    expires_at  TEXT    NOT NULL      -- ISO 8601 UTC
);

CREATE INDEX idx_sessions_token      ON sessions(token);
CREATE INDEX idx_sessions_user_id    ON sessions(user_id);
CREATE INDEX idx_sessions_expires_at ON sessions(expires_at);

INSERT INTO schema_migrations(version, applied_at)
VALUES (6, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));
