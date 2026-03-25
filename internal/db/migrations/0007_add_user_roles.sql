-- Migration: 0007
-- Adds user_roles table for role-based access control.
--
-- Four fixed roles: admin, operator, approver, auditor.
-- Users may hold multiple roles simultaneously.
--
-- Data migration: existing users (created before this migration) are assigned
-- the 'admin' role to prevent lock-out on upgrade.

CREATE TABLE user_roles (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK(role IN ('admin', 'operator', 'approver', 'auditor')),
    created_at TEXT NOT NULL,  -- ISO 8601 UTC
    PRIMARY KEY (user_id, role)
);

CREATE INDEX idx_user_roles_user_id ON user_roles(user_id);

-- Assign admin role to all existing users to prevent lock-out on upgrade.
INSERT INTO user_roles (user_id, role, created_at)
SELECT id, 'admin', strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
FROM users;

INSERT INTO schema_migrations(version, applied_at)
VALUES (7, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));
