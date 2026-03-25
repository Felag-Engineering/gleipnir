-- Gleipnir — Initial Schema
-- Migration: 0001
-- Applied by: startup migration runner on first boot
--
-- Design decisions:
--   ADR-002: Policy-as-YAML stored in DB. name and trigger_type as columns for
--            list views and trigger routing; everything else lives in the YAML.
--   ADR-003: SQLite, WAL mode enabled at the application layer on startup.
--   ADR-007: sensor / actuator / feedback roles stored as capability_role on
--            mcp_tools (denormalized — each tool has exactly one role).
--   ADR-011: interrupted is a valid terminal run state. Startup scan marks any
--            run in running or waiting_for_approval as interrupted.
--   ADR-012: Prompt fields (preamble, task instructions) live in the policy YAML.
--            The capabilities block is generated at run start and never persisted.
--   ADR-013: IDs are ULIDs (TEXT). Timestamps are ISO 8601 UTC (TEXT).

PRAGMA foreign_keys = ON;

-- ---------------------------------------------------------------------------
-- Schema version tracking
-- ---------------------------------------------------------------------------

CREATE TABLE schema_migrations (
    version     INTEGER PRIMARY KEY,
    applied_at  TEXT    NOT NULL  -- ISO 8601 UTC
);

-- ---------------------------------------------------------------------------
-- MCP servers
-- ---------------------------------------------------------------------------

CREATE TABLE mcp_servers (
    id                  TEXT    PRIMARY KEY,  -- ULID
    name                TEXT    NOT NULL UNIQUE,
    url                 TEXT    NOT NULL,
    last_discovered_at  TEXT,                 -- nullable, ISO 8601 UTC
    created_at          TEXT    NOT NULL      -- ISO 8601 UTC
);

-- ---------------------------------------------------------------------------
-- MCP tools
--
-- capability_role is denormalized onto the tool row. Each tool has exactly
-- one role. The separate capability_tags table mentioned in early design docs
-- was collapsed here — a join table bought nothing given the 1:1 relationship.
-- ---------------------------------------------------------------------------

CREATE TABLE mcp_tools (
    id              TEXT    PRIMARY KEY,  -- ULID
    server_id       TEXT    NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    name            TEXT    NOT NULL,
    description     TEXT    NOT NULL,
    input_schema    TEXT    NOT NULL,     -- JSON blob (MCP tool input schema)
    capability_role TEXT    NOT NULL CHECK(capability_role IN ('sensor', 'actuator', 'feedback')),
    created_at      TEXT    NOT NULL,     -- ISO 8601 UTC
    UNIQUE(server_id, name)
);

CREATE INDEX idx_mcp_tools_server_id ON mcp_tools(server_id);

-- ---------------------------------------------------------------------------
-- Policies
--
-- name and trigger_type are columns for fast list views and trigger routing.
-- All other configuration (capabilities, prompt fields, run limits, concurrency,
-- feedback channel) lives in the yaml column — single source of truth.
-- ---------------------------------------------------------------------------

CREATE TABLE policies (
    id              TEXT    PRIMARY KEY,  -- ULID
    name            TEXT    NOT NULL UNIQUE,
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'cron', 'poll', 'manual', 'scheduled')),
    yaml            TEXT    NOT NULL,
    created_at      TEXT    NOT NULL,     -- ISO 8601 UTC
    updated_at      TEXT    NOT NULL,     -- ISO 8601 UTC
    paused_at       TEXT                  -- nullable; set when a scheduled policy exhausts all fire_at times
);

CREATE INDEX idx_policies_trigger_type ON policies(trigger_type);

-- ---------------------------------------------------------------------------
-- Runs
--
-- trigger_payload is the webhook body / cron metadata / poll result that
-- caused this run. Stored as a JSON blob. Delivered to the agent as the
-- first user message at run start (ADR-012).
--
-- thread_id is nullable — reserved for future Slack threading (EPIC-010).
-- A Slack thread_ts is written here when the first approval or notification
-- message is posted for a run.
--
-- token_cost accumulates across all steps. Updated on each step write.
--
-- error is only populated on terminal failed or interrupted states.
-- ---------------------------------------------------------------------------

CREATE TABLE runs (
    id              TEXT    PRIMARY KEY,  -- ULID
    policy_id       TEXT    NOT NULL REFERENCES policies(id),
    status          TEXT    NOT NULL CHECK(status IN (
                        'pending',
                        'running',
                        'waiting_for_approval',
                        'complete',
                        'failed',
                        'interrupted'
                    )),
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'cron', 'poll', 'manual', 'scheduled')),
    trigger_payload TEXT    NOT NULL,     -- JSON blob
    started_at      TEXT    NOT NULL,     -- ISO 8601 UTC
    completed_at    TEXT,                 -- nullable, ISO 8601 UTC
    token_cost      INTEGER NOT NULL DEFAULT 0,
    error           TEXT,                 -- nullable, terminal error message
    thread_id       TEXT,                 -- nullable, Slack thread_ts
    created_at      TEXT    NOT NULL,     -- ISO 8601 UTC
    system_prompt   TEXT                  -- nullable, rendered system prompt at run start
);

CREATE INDEX idx_runs_policy_id      ON runs(policy_id);
CREATE INDEX idx_runs_status         ON runs(status);
CREATE INDEX idx_runs_created_at     ON runs(created_at DESC);
CREATE INDEX idx_runs_policy_created ON runs(policy_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- Run steps
--
-- Full reasoning trace. One row per step in the agent conversation loop.
-- step_number is 1-indexed and contiguous within a run.
--
-- type discriminates the content shape:
--
--   thought          { "text": "..." }
--   tool_call        { "tool_name": "...", "server_id": "...", "input": {...} }
--   tool_result      { "tool_name": "...", "output": ..., "is_error": false }
--   approval_request { "approval_request_id": "..." }
--   feedback_request { "message": "..." }
--   feedback_response{ "response": "..." }
--   error            { "message": "...", "code": "..." }
--   complete         { "summary": "..." }
--
-- content is a raw JSON blob. No typed columns — the reasoning timeline is
-- read sequentially by run_id; there are no current query patterns that
-- require filtering steps by tool name or content fields. Add typed columns
-- in a later migration if observability queries need them.
--
-- token_cost is 0 for non-LLM steps (tool_result, approval_request, etc).
-- Accumulated into runs.token_cost on each write.
--
-- Writes are serialized through an application-layer queue to avoid
-- contention under concurrent runs (ADR-003).
-- ---------------------------------------------------------------------------

CREATE TABLE run_steps (
    id          TEXT    PRIMARY KEY,  -- ULID
    run_id      TEXT    NOT NULL REFERENCES runs(id),
    step_number INTEGER NOT NULL,
    type        TEXT    NOT NULL CHECK(type IN (
                    'capability_snapshot',
                    'thought',
                    'tool_call',
                    'tool_result',
                    'approval_request',
                    'feedback_request',
                    'feedback_response',
                    'error',
                    'complete'
                )),
    content     TEXT    NOT NULL,     -- JSON blob, shape varies by type
    token_cost  INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL,     -- ISO 8601 UTC
    UNIQUE(run_id, step_number)
);

CREATE INDEX idx_run_steps_run_step ON run_steps(run_id, step_number);

-- ---------------------------------------------------------------------------
-- Approval requests
--
-- Created when the approval interceptor pauses a run before an actuator
-- call marked approval: required.
--
-- reasoning_summary is a snapshot of the run's reasoning up to the pause
-- point — rendered from recent run_steps at intercept time and stored here
-- so the approval UI doesn't need to re-derive it.
--
-- expires_at is computed from the policy's approval_timeout at creation time.
-- The background timeout scanner compares expires_at against current time.
--
-- note is the operator's optional comment on approve or reject.
-- ---------------------------------------------------------------------------

CREATE TABLE approval_requests (
    id                TEXT    PRIMARY KEY,  -- ULID
    run_id            TEXT    NOT NULL REFERENCES runs(id),
    tool_name         TEXT    NOT NULL,
    proposed_input    TEXT    NOT NULL,     -- JSON blob
    reasoning_summary TEXT    NOT NULL,
    status            TEXT    NOT NULL CHECK(status IN (
                          'pending',
                          'approved',
                          'rejected',
                          'timeout'
                      )),
    decided_at        TEXT,                 -- nullable, ISO 8601 UTC
    expires_at        TEXT    NOT NULL,     -- ISO 8601 UTC
    note              TEXT,                 -- nullable
    created_at        TEXT    NOT NULL      -- ISO 8601 UTC
);

CREATE INDEX idx_approval_requests_run_id          ON approval_requests(run_id);
CREATE INDEX idx_approval_requests_status          ON approval_requests(status);
CREATE INDEX idx_approval_requests_status_expires  ON approval_requests(status, expires_at);
CREATE INDEX idx_approval_requests_run_pending     ON approval_requests(run_id, status);

-- ---------------------------------------------------------------------------
-- Users
--
-- deactivated_at is nullable — a non-null value means the account has been
-- soft-deleted and must not be used for login.
-- ---------------------------------------------------------------------------

CREATE TABLE users (
    id              TEXT    PRIMARY KEY,  -- ULID
    username        TEXT    NOT NULL UNIQUE,
    password_hash   TEXT    NOT NULL,
    created_at      TEXT    NOT NULL,     -- ISO 8601 UTC
    deactivated_at  TEXT                  -- nullable, ISO 8601 UTC
);

-- ---------------------------------------------------------------------------
-- Sessions
--
-- token is a random opaque value stored in a cookie. The index on token is
-- the hot path for every authenticated request.
-- ---------------------------------------------------------------------------

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

-- ---------------------------------------------------------------------------
-- User roles
--
-- Four fixed roles: admin, operator, approver, auditor.
-- Users may hold multiple roles simultaneously.
-- ---------------------------------------------------------------------------

CREATE TABLE user_roles (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK(role IN ('admin', 'operator', 'approver', 'auditor')),
    created_at TEXT NOT NULL,  -- ISO 8601 UTC
    PRIMARY KEY (user_id, role)
);

CREATE INDEX idx_user_roles_user_id ON user_roles(user_id);

-- ---------------------------------------------------------------------------
-- Seed migration version
-- ---------------------------------------------------------------------------

-- Seed all migration versions that are baked into this initial schema.
INSERT INTO schema_migrations(version, applied_at) VALUES (1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));
INSERT INTO schema_migrations(version, applied_at) VALUES (2, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));
INSERT INTO schema_migrations(version, applied_at) VALUES (3, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));
INSERT INTO schema_migrations(version, applied_at) VALUES (4, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));
INSERT INTO schema_migrations(version, applied_at) VALUES (5, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));
INSERT INTO schema_migrations(version, applied_at) VALUES (6, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));
INSERT INTO schema_migrations(version, applied_at) VALUES (7, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));
