-- Migration: 0010
-- Adds user_preferences table and user_agent/ip_address columns to sessions.
-- Applied by the startup migration runner on existing deployments.
-- New deployments get both from 0001_initial.sql directly.

CREATE TABLE IF NOT EXISTS user_preferences (
    user_id          TEXT NOT NULL REFERENCES users(id),
    preference_key   TEXT NOT NULL,
    preference_value TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    UNIQUE(user_id, preference_key)
);
