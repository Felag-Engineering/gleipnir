-- Gleipnir — Initial Schema
-- Migration: 0001
-- Applied by: startup migration runner on first boot
--
-- Design decisions:
--   ADR-002: Policy-as-YAML stored in DB. name and trigger_type as columns for
--            list views and trigger routing; everything else lives in the YAML.
--   ADR-003: SQLite, WAL mode enabled at the application layer on startup.
--   ADR-007: tool / feedback roles stored as capability_role on
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
    has_drift           INTEGER NOT NULL DEFAULT 0,  -- 1 when re-discovery found changes
    created_at          TEXT    NOT NULL      -- ISO 8601 UTC
);

-- ---------------------------------------------------------------------------
-- MCP tools
-- ---------------------------------------------------------------------------

CREATE TABLE mcp_tools (
    id              TEXT    PRIMARY KEY,  -- ULID
    server_id       TEXT    NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    name            TEXT    NOT NULL,
    description     TEXT    NOT NULL,
    input_schema    TEXT    NOT NULL,     -- JSON blob (MCP tool input schema)
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
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'manual', 'scheduled', 'poll')),
    yaml            TEXT    NOT NULL,
    created_at      TEXT    NOT NULL,     -- ISO 8601 UTC
    updated_at      TEXT    NOT NULL,     -- ISO 8601 UTC
    paused_at       TEXT                  -- nullable, ISO 8601 UTC; set when a scheduled policy exhausts all fire times
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
    policy_id       TEXT    NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
    status          TEXT    NOT NULL CHECK(status IN (
                        'pending',
                        'running',
                        'waiting_for_approval',
                        'waiting_for_feedback',
                        'complete',
                        'failed',
                        'interrupted'
                    )),
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'manual', 'scheduled', 'poll')),
    trigger_payload TEXT    NOT NULL,     -- JSON blob
    started_at      TEXT    NOT NULL,     -- ISO 8601 UTC
    completed_at    TEXT,                 -- nullable, ISO 8601 UTC
    token_cost      INTEGER NOT NULL DEFAULT 0,
    error           TEXT,                 -- nullable, terminal error message
    thread_id       TEXT,                 -- nullable, Slack thread_ts
    created_at      TEXT    NOT NULL,     -- ISO 8601 UTC
    system_prompt   TEXT,                 -- nullable, rendered system prompt at run start
    model           TEXT    NOT NULL DEFAULT ''  -- API model ID (e.g. claude-sonnet-4-6); empty for legacy runs
);

CREATE INDEX idx_runs_status         ON runs(status);
CREATE INDEX idx_runs_created_at     ON runs(created_at DESC);
CREATE INDEX idx_runs_policy_created ON runs(policy_id, created_at DESC);
CREATE INDEX idx_runs_policy_status  ON runs(policy_id, status);

-- ---------------------------------------------------------------------------
-- Run steps
--
-- Full reasoning trace. One row per step in the agent conversation loop.
-- step_number is 0-indexed and contiguous within a run; step 0 is always capability_snapshot.
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
    run_id      TEXT    NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
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
-- Created when the approval interceptor pauses a run before a tool
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
    run_id            TEXT    NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
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

CREATE INDEX idx_approval_requests_run_id         ON approval_requests(run_id);
CREATE INDEX idx_approval_requests_status         ON approval_requests(status);
CREATE INDEX idx_approval_requests_status_expires ON approval_requests(status, expires_at);
CREATE INDEX idx_approval_requests_run_pending    ON approval_requests(run_id, status);

-- ---------------------------------------------------------------------------
-- Feedback requests
-- ---------------------------------------------------------------------------

CREATE TABLE feedback_requests (
    id              TEXT    PRIMARY KEY,  -- ULID
    run_id          TEXT    NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    tool_name       TEXT    NOT NULL,
    proposed_input  TEXT    NOT NULL,     -- JSON blob
    message         TEXT    NOT NULL,     -- MCP tool output (notification sent to operator)
    status          TEXT    NOT NULL CHECK(status IN ('pending', 'resolved', 'timed_out')),
    response        TEXT,                 -- nullable, operator's freeform text response
    resolved_at     TEXT,                 -- nullable, ISO 8601 UTC
    expires_at      TEXT,                 -- nullable, ISO 8601 UTC; set when a timeout is configured
    created_at      TEXT    NOT NULL      -- ISO 8601 UTC
);

CREATE INDEX idx_feedback_requests_run_id         ON feedback_requests(run_id);
CREATE INDEX idx_feedback_requests_status         ON feedback_requests(status);
CREATE INDEX idx_feedback_requests_run_pending    ON feedback_requests(run_id, status);
CREATE INDEX idx_feedback_requests_status_expires ON feedback_requests(status, expires_at);

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

-- ---------------------------------------------------------------------------
-- Trigger queue
--
-- Holds enqueued trigger payloads for policies with concurrency: queue.
-- When a run is active, incoming triggers are appended here and dequeued
-- (FIFO by position) when the active run reaches a terminal state.
--
-- position is MAX(position)+1 per policy — it grows monotonically and is
-- never renumbered after dequeues. Harmless at small queue depths.
-- ---------------------------------------------------------------------------

CREATE TABLE trigger_queue (
    id              TEXT    PRIMARY KEY,  -- ULID
    policy_id       TEXT    NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'manual', 'scheduled', 'poll')),
    trigger_payload TEXT    NOT NULL,     -- JSON blob
    position        INTEGER NOT NULL,     -- monotonically increasing per-policy ordering
    created_at      TEXT    NOT NULL,     -- ISO 8601 UTC
    UNIQUE(policy_id, position)
);

CREATE INDEX idx_trigger_queue_policy_position ON trigger_queue(policy_id, position);

-- ---------------------------------------------------------------------------
-- Poll states
--
-- Tracks per-policy polling state for policies with trigger_type = 'poll'.
-- next_poll_at drives scheduling; last_result_hash prevents re-triggering
-- an identical result on restart.
-- consecutive_failures drives exponential back-off.
-- ---------------------------------------------------------------------------

CREATE TABLE poll_states (
    policy_id            TEXT    PRIMARY KEY REFERENCES policies(id) ON DELETE CASCADE,
    last_poll_at         TEXT,                 -- nullable, ISO 8601 UTC
    last_result_hash     TEXT,                 -- nullable, SHA-256 hex of last non-empty result
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    next_poll_at         TEXT    NOT NULL,      -- ISO 8601 UTC, used by the poller to schedule
    created_at           TEXT    NOT NULL,      -- ISO 8601 UTC
    updated_at           TEXT    NOT NULL       -- ISO 8601 UTC
);

-- ---------------------------------------------------------------------------
-- Seed migration version
-- ---------------------------------------------------------------------------

INSERT INTO schema_migrations(version, applied_at) VALUES (1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));

-- System-wide key-value settings (API keys encrypted, default model, run limits).
CREATE TABLE system_settings (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

-- Per-model enablement. No row = enabled by default.
CREATE TABLE model_settings (
    provider    TEXT    NOT NULL,
    model_name  TEXT    NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    updated_at  TEXT    NOT NULL,
    PRIMARY KEY (provider, model_name)
);

-- OpenAI-compatible provider instances (ADR-032).
-- Each row is a runtime-registered LLM provider speaking the OpenAI Chat
-- Completions wire format. Admins create, edit, and delete these from the
-- admin UI. Names `anthropic` and `google` are reserved by the API layer.
CREATE TABLE openai_compat_providers (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT    NOT NULL UNIQUE,
    base_url          TEXT    NOT NULL,
    api_key_encrypted TEXT    NOT NULL,
    created_at        TEXT    NOT NULL,
    updated_at        TEXT    NOT NULL
);

CREATE INDEX idx_openai_compat_providers_name ON openai_compat_providers(name);
