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
    id                      TEXT    PRIMARY KEY,  -- ULID
    name                    TEXT    NOT NULL UNIQUE,
    url                     TEXT    NOT NULL,
    last_discovered_at      TEXT,                 -- nullable, ISO 8601 UTC
    has_drift               INTEGER NOT NULL DEFAULT 0,  -- 1 when re-discovery found changes
    created_at              TEXT    NOT NULL,     -- ISO 8601 UTC
    -- Encrypted auth headers (AES-256-GCM, key from GLEIPNIR_ENCRYPTION_KEY).
    -- JSON array of {"name":"...","value":"..."} objects, serialized then encrypted.
    -- Values are write-only; only header names are returned via the API (ADR-039).
    auth_headers_encrypted  TEXT,                 -- nullable; TEXT stores base64 ciphertext
    -- Negotiated MCP protocol version pinned at server/discover time
    -- (mcp-realignment-spec.md §11). NULL = not yet probed.
    protocol_version        TEXT                  -- nullable; e.g. '2026-07-28'
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
    enabled         INTEGER NOT NULL DEFAULT 1,  -- operator-managed; 0 = disabled, 1 = enabled
    canonical_schema TEXT,  -- nullable; schemanorm-normalized input_schema (ADR-059); NULL = not normalized
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
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'manual', 'scheduled', 'poll', 'cron', 'subscribed')),
    yaml            TEXT    NOT NULL,
    -- Encrypted webhook shared secret (AES-256-GCM, key from GLEIPNIR_ENCRYPTION_KEY).
    -- Stored outside yaml because yaml is returned wholesale via GET /api/v1/policies/:id — see ADR-034.
    webhook_secret_encrypted TEXT,
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
--
-- version is an optimistic-lock counter bumped by every status-changing UPDATE.
-- Callers supply the expected version; rows_affected == 0 means another writer
-- already advanced the row (ErrTransitionConflict). See ADR-038.
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
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'manual', 'scheduled', 'poll', 'cron', 'subscribed')),
    trigger_payload TEXT    NOT NULL,     -- JSON blob
    started_at      TEXT    NOT NULL,     -- ISO 8601 UTC
    completed_at    TEXT,                 -- nullable, ISO 8601 UTC
    token_cost      INTEGER NOT NULL DEFAULT 0,
    error           TEXT,                 -- nullable, terminal error message
    thread_id       TEXT,                 -- nullable, Slack thread_ts
    created_at      TEXT    NOT NULL,     -- ISO 8601 UTC
    system_prompt   TEXT,                 -- nullable, rendered system prompt at run start
    model           TEXT    NOT NULL DEFAULT '',  -- API model ID (e.g. claude-sonnet-4-6); empty for legacy runs
    version         INTEGER NOT NULL DEFAULT 0   -- optimistic-lock counter; bumped on every status UPDATE
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
--   thinking         { "text": "..." }   (extended/redacted thinking blocks)
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
                    'thinking',
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
    deactivated_at  TEXT,                 -- nullable, ISO 8601 UTC
    slack_user_id   TEXT    UNIQUE        -- nullable; one Slack workspace user id ↔ at most one Gleipnir user
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_slack_user_id ON users(slack_user_id);

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
    expires_at  TEXT    NOT NULL,     -- ISO 8601 UTC
    user_agent  TEXT    NOT NULL DEFAULT '',  -- captured at login for the active-sessions UI
    ip_address  TEXT    NOT NULL DEFAULT ''   -- captured at login for the active-sessions UI
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
    trigger_type    TEXT    NOT NULL CHECK(trigger_type IN ('webhook', 'manual', 'scheduled', 'poll', 'cron', 'subscribed')),
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
-- User preferences
--
-- Key-value store for per-user UI preferences (e.g. timezone). Keys are
-- validated at the application layer; not constrained in the schema.
-- ---------------------------------------------------------------------------

CREATE TABLE user_preferences (
    user_id          TEXT NOT NULL REFERENCES users(id),
    preference_key   TEXT NOT NULL,
    preference_value TEXT NOT NULL,
    updated_at       TEXT NOT NULL,  -- ISO 8601 UTC
    UNIQUE(user_id, preference_key)
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

-- ---------------------------------------------------------------------------
-- Plugin system (ADR-041, ADR-045, ADR-046)
--
-- Three tables back the v1 plugin system:
--   - plugins:             one row per installed plugin (binary + manifest).
--   - plugin_instances:    one row per configured deployment of a plugin.
--   - plugin_audit_events: operator-only audit trail for plugin lifecycle
--                          (install, signature verification, key rotations,
--                          credential lifecycle, late callbacks). NEVER
--                          surfaced to the LLM — see ADR-046.
--
-- ADR-045 captures the trusted_pubkey on each plugin row at first install
-- (TOFU). The manifest_snapshot column is the canonical manifest captured at
-- install/approval; hot-reload diffs against this column to detect material
-- changes (spec §5.4). The status column drives the install-flow state
-- machine: a freshly installed plugin sits in pending_review until an admin
-- clicks through; uninstall keeps the row but flips status to removed so
-- audit history (FK from plugin_audit_events) survives.
--
-- The version column on plugins and plugin_instances is the ADR-038 CAS
-- counter (do not confuse with plugin_version, which is the plugin author's
-- own SemVer string from the manifest). Health-state transitions on
-- plugin_instances inherit ADR-038 semantics for free.
-- ---------------------------------------------------------------------------

CREATE TABLE plugins (
    id                 TEXT    PRIMARY KEY,                                              -- ULID
    name               TEXT    NOT NULL UNIQUE,                                          -- manifest name; stable identity for TOFU
    plugin_version     TEXT    NOT NULL,                                                 -- author's SemVer (ADR-042)
    manifest_snapshot  TEXT    NOT NULL,                                                 -- canonical manifest JSON (signed payload component)
    trusted_pubkey     TEXT    NOT NULL,                                                 -- Minisign Ed25519 pubkey, TOFU-captured (ADR-045)
    status             TEXT    NOT NULL CHECK(status IN ('pending_review','active','removed')),
    binary_path        TEXT,                                                             -- absolute path to extracted plugin executable (#386); NULL on legacy rows
    version            INTEGER NOT NULL DEFAULT 0,                                       -- ADR-038 CAS counter
    created_at         TEXT    NOT NULL,                                                 -- ISO 8601 UTC
    updated_at         TEXT    NOT NULL                                                  -- ISO 8601 UTC
);

CREATE INDEX idx_plugins_status ON plugins(status);

CREATE TABLE plugin_instances (
    id                       TEXT    PRIMARY KEY,                                        -- ULID
    plugin_id                TEXT    NOT NULL REFERENCES plugins(id) ON DELETE CASCADE,
    instance_name            TEXT    NOT NULL,                                           -- e.g. "slack-prod"
    config_json              TEXT    NOT NULL DEFAULT '{}',                              -- per-instance config validated against plugin's config_schema
    subscription_scope_json  TEXT    NOT NULL DEFAULT '{}',                              -- coarse subscription scope sent to plugin via TriggerService.Start (spec §4.3, #223)
    -- Encrypted credentials (AES-256-GCM, key from GLEIPNIR_ENCRYPTION_KEY).
    -- Reuses the existing crypto helpers; mirrors ADR-039 / ADR-034 write-only
    -- pattern: GET endpoints expose presence only, never decrypted values.
    credentials_encrypted    TEXT,                                                        -- nullable; base64 ciphertext
    credentials_expires_at   TEXT,                                                        -- nullable; OAuth token refresh hook (#227)
    handshake_versions       TEXT    NOT NULL DEFAULT '{}',                              -- JSON: per-service version pin from §10.3 handshake
    health_state             TEXT    NOT NULL DEFAULT 'pending_key_approval'
                                     CHECK(health_state IN (
                                         'healthy',
                                         'signature_invalid',
                                         'pending_key_approval',
                                         'pending_manifest_approval',
                                         'pending_config_migration',
                                         'verification_error',
                                         'unsigned_permissive',
                                         'unhealthy',
                                         'crashed',
                                         'circuit_broken',
                                         'pending_reauthorize',
                                         'inactive'
                                     )),                                                  -- ADR-038, ADR-045 §7, issue #191, #230, #243
    health_detail            TEXT,                                                        -- nullable; operator-facing reason for non-healthy state
    last_oauth_callback_url  TEXT,                                                        -- nullable; OAuth-flow plumbing (#230)
    host_event_rate_per_sec  REAL,                                                        -- nullable; host-owned EmitEvent sustained rate (events/sec); NULL → default 100 (#577)
    host_event_burst         INTEGER,                                                     -- nullable; host-owned EmitEvent burst ceiling; NULL → default 200 (#577)
    version                  INTEGER NOT NULL DEFAULT 0,                                  -- ADR-038 CAS counter
    created_at               TEXT    NOT NULL,                                            -- ISO 8601 UTC
    updated_at               TEXT    NOT NULL,                                            -- ISO 8601 UTC
    UNIQUE (plugin_id, instance_name)
);

CREATE INDEX idx_plugin_instances_plugin_id    ON plugin_instances(plugin_id);
CREATE INDEX idx_plugin_instances_health_state ON plugin_instances(health_state);

CREATE TABLE plugin_audit_events (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    plugin_instance_id  TEXT    REFERENCES plugin_instances(id) ON DELETE SET NULL,      -- nullable: pre-install + plugin-level events
    event_type          TEXT    NOT NULL,                                                  -- e.g. 'plugin_installed', 'pubkey_rotated', 'feedback_response_late'
    severity            TEXT    NOT NULL CHECK(severity IN ('info','warning','high','critical')),
    actor_user_id       TEXT    REFERENCES users(id) ON DELETE SET NULL,                 -- nullable: system-driven events have no human actor
    payload_json        TEXT    NOT NULL,                                                  -- structured event detail
    created_at          TEXT    NOT NULL                                                   -- ISO 8601 UTC
);

CREATE INDEX idx_pae_instance_created ON plugin_audit_events(plugin_instance_id, created_at);
CREATE INDEX idx_pae_event_created    ON plugin_audit_events(event_type, created_at);

-- ---------------------------------------------------------------------------
-- Plugin pending manifests
--
-- Holds the single pending candidate manifest per plugin while it awaits admin
-- approval. PRIMARY KEY(plugin_id) enforces at-most-one pending candidate;
-- ON DELETE CASCADE clears the row automatically when a plugin is uninstalled.
-- Candidate bytes are stored raw (NOT base64) — they only lived in the audit
-- event payload as base64 for JSON embedding; the table is the new source of
-- truth. Accepted via POST /api/v1/admin/plugins/{id}/accept-manifest, after
-- which the row is deleted best-effort (leftover rows self-correct on next
-- material change via the ON CONFLICT DO UPDATE upsert).
-- ---------------------------------------------------------------------------

CREATE TABLE plugin_pending_manifests (
    plugin_id          TEXT PRIMARY KEY REFERENCES plugins(id) ON DELETE CASCADE,
    candidate_manifest TEXT NOT NULL,   -- raw candidate manifest bytes (NOT base64)
    old_version        TEXT NOT NULL,
    new_version        TEXT NOT NULL,
    created_at         TEXT NOT NULL,   -- ISO 8601 UTC
    updated_at         TEXT NOT NULL    -- ISO 8601 UTC
);

-- ---------------------------------------------------------------------------
-- Plugin audiences and pending requests (spec §6.1, §4.2, §11.5)
--
-- plugin_audiences — first-class shared resources. Each audience is a named
--   ordered list of plugin-instance entries. The version column is the
--   ADR-038 CAS counter for atomic edits.
--
-- audience_entries — ordered member list for an audience. The position
--   column drives ordering; the UNIQUE constraint on (audience_id, position)
--   is NOT DEFERRABLE (modernc.org/sqlite rejects DEFERRABLE on table
--   constraints). Multi-row reorders inside a single transaction must use a
--   temporary sentinel position to avoid transient violations. CASCADE from
--   plugin_audiences so deleting an audience removes its entries. RESTRICT
--   from plugin_instances so an instance cannot be uninstalled while
--   referenced (§11.5 uninstall gate).
--
-- plugin_pending_requests — tracks ChannelService.Request rows once
--   pre-ack has succeeded. Status enum mirrors feedback_requests so
--   internal/timeout/scanner.go can drive both tables. The (status,
--   expires_at) index reuses the same access pattern as feedback_requests.
--   audience_entry_id is nullable with SET NULL so in-flight Channel
--   Requests continue to resolve after an entry is edited/deleted (§11.7).
--   No CASCADE from plugin_instances — uninstall is blocked upstream by
--   §11.5; an orphaned row should never exist but must not be silently
--   deleted. tool_name mirrors feedback_requests.tool_name so the timeout
--   scanner has the field it reads when building ExpiredItem values.
-- ---------------------------------------------------------------------------

CREATE TABLE plugin_audiences (
    id                      TEXT    PRIMARY KEY,                             -- ULID (ADR-013)
    name                    TEXT    NOT NULL UNIQUE,
    created_by_user_id      TEXT    REFERENCES users(id) ON DELETE SET NULL,
    version                 INTEGER NOT NULL DEFAULT 0,                      -- ADR-038 CAS counter
    created_at              TEXT    NOT NULL,                                -- ISO 8601 UTC
    updated_at              TEXT    NOT NULL,                                -- ISO 8601 UTC
    disable_in_app_fallback INTEGER NOT NULL DEFAULT 0                       -- §6.2 opt-out toggle
);

CREATE TABLE audience_entries (
    id                 TEXT    PRIMARY KEY,                                  -- ULID
    audience_id        TEXT    NOT NULL REFERENCES plugin_audiences(id) ON DELETE CASCADE,
    plugin_instance_id TEXT    NOT NULL REFERENCES plugin_instances(id) ON DELETE RESTRICT,
    position           INTEGER NOT NULL,
    notify             INTEGER NOT NULL DEFAULT 0,
    request            INTEGER NOT NULL DEFAULT 0,
    config_json        TEXT    NOT NULL DEFAULT '{}',
    UNIQUE (audience_id, position)
);
CREATE INDEX idx_audience_entries_audience  ON audience_entries(audience_id);
CREATE INDEX idx_audience_entries_instance  ON audience_entries(plugin_instance_id);

CREATE TABLE plugin_pending_requests (
    id                  TEXT    PRIMARY KEY,                                 -- ULID; spec's request_id
    plugin_instance_id  TEXT    NOT NULL REFERENCES plugin_instances(id) ON DELETE RESTRICT,
    run_id              TEXT    NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    audience_entry_id   TEXT    REFERENCES audience_entries(id) ON DELETE SET NULL,
    tool_name           TEXT    NOT NULL DEFAULT '',
    status              TEXT    NOT NULL CHECK(status IN ('pending','resolved','timed_out')),
    response            TEXT,
    expires_at          TEXT,
    resolved_at         TEXT,
    created_at          TEXT    NOT NULL
);
CREATE INDEX idx_plugin_pending_requests_run_status      ON plugin_pending_requests(run_id, status);
CREATE INDEX idx_plugin_pending_requests_status_expires  ON plugin_pending_requests(status, expires_at);

CREATE TABLE plugin_oauth_nonces (
    nonce       TEXT PRIMARY KEY,           -- base64url-encoded 32B random
    instance_id TEXT NOT NULL,              -- not FK-enforced: instance may have been uninstalled mid-flow
    expires_at  TEXT NOT NULL,              -- RFC3339Nano UTC; pruned by janitor
    created_at  TEXT NOT NULL
) STRICT;
CREATE INDEX plugin_oauth_nonces_expires_at_idx ON plugin_oauth_nonces (expires_at);

CREATE TABLE plugin_event_dedup (
    plugin_instance_id TEXT    NOT NULL REFERENCES plugin_instances(id) ON DELETE CASCADE,
    event_kind         TEXT    NOT NULL,
    event_id           TEXT    NOT NULL,
    created_at_ms      INTEGER NOT NULL,   -- host-assigned Unix millis; eviction orders on THIS
    PRIMARY KEY (plugin_instance_id, event_kind, event_id)
) WITHOUT ROWID;
CREATE INDEX idx_plugin_event_dedup_created_at_ms ON plugin_event_dedup(created_at_ms);

-- ---------------------------------------------------------------------------
-- Tool-initiated HITL and MCP task handles (ADR-055, mcp-realignment-spec.md §6)
--
-- tool_input_requests persists a tool-initiated human-in-the-loop wait: an MCP
-- server paused a tools/call with an MRTR input_required result (§6, §6.6).
-- The row records the original call (server, tool, args) so it can be
-- retried, the opaque requestState the server returned (size-capped at write,
-- defense in depth per the §6.2 abuse controls), the elicitation-shaped
-- request payload (messages + schemas), the elicitation_kind split from §6.1
-- (permission vs information), and the effective deadline computed per the
-- §6.3 timeout precedence (Gleipnir policy timeout is authoritative). This is
-- what lets a human answer be applied after a host restart -- the §13
-- durability claim.
--
-- mcp_tasks persists MCP Tasks-extension handles (SEP-2663) so polling
-- resumes after a restart. kind covers both tool_call (a task backing a
-- tool_input_requests row) and channel_request -- the same table is designed
-- for reuse by the eventual §6.4 Channel-Request-as-task path, not just
-- tool-initiated waits. server_ttl is "weather" (§6.3): expiry there is a
-- distinct, well-explained failure, never conflated with a task the server
-- itself failed.
-- ---------------------------------------------------------------------------

CREATE TABLE tool_input_requests (
    id                TEXT    PRIMARY KEY,                                              -- ULID
    run_id            TEXT    NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    server_id         TEXT    NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,     -- server that owns the original tools/call
    tool_name         TEXT    NOT NULL,                                                 -- original tools/call name
    call_args         TEXT    NOT NULL,                                                 -- JSON blob, original tools/call arguments
    request_state     TEXT    NOT NULL,                                                 -- opaque MRTR requestState from the server's InputRequiredResult; size-capped at write (defense in depth, application layer, spec §6.2)
    request_payload   TEXT    NOT NULL,                                                 -- JSON blob: elicitation-shaped payload (messages + inputRequests/requestedSchema)
    elicitation_kind  TEXT    NOT NULL CHECK(elicitation_kind IN ('permission', 'information')),  -- spec §6.1
    status            TEXT    NOT NULL CHECK(status IN ('pending', 'resolved', 'timed_out')),
    response          TEXT,                                                             -- nullable, JSON blob of inputResponses / operator answer
    resolved_at       TEXT,                                                             -- nullable, ISO 8601 UTC
    expires_at        TEXT    NOT NULL,                                                 -- effective deadline: min of Gleipnir policy timeout / server TTL / requestState TTL (spec §6.3)
    deadline_source   TEXT    CHECK(deadline_source IS NULL OR deadline_source IN ('policy', 'server_ttl', 'request_state')),  -- which clock produced expires_at; NULL means written before the column existed
    replay_context    TEXT,                                                             -- nullable, JSON blob: the question + answer that preceded this one when a server re-asked differently after its MRTR state expired (spec §6.5); NULL means this is a first ask
    created_at        TEXT    NOT NULL                                                  -- ISO 8601 UTC
);
CREATE INDEX idx_tool_input_requests_run_id         ON tool_input_requests(run_id);
CREATE INDEX idx_tool_input_requests_run_pending    ON tool_input_requests(run_id, status);
CREATE INDEX idx_tool_input_requests_status_expires ON tool_input_requests(status, expires_at);

CREATE TABLE mcp_tasks (
    id                TEXT    PRIMARY KEY,                                              -- ULID
    run_id            TEXT    NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    server_id         TEXT    REFERENCES mcp_servers(id) ON DELETE CASCADE,              -- nullable: NULL means an internal (in-app) task with no MCP server behind it (spec §6.4)
    task_id           TEXT    NOT NULL,                                                 -- server-assigned Tasks-extension task id (SEP-2663)
    kind              TEXT    NOT NULL CHECK(kind IN ('tool_call', 'channel_request')),  -- reused by both the tool-initiated wait path and the eventual channel-Request-as-task path (spec §6.4)
    poll_interval_ms  INTEGER,                                                          -- nullable; server-suggested poll cadence
    server_ttl        TEXT,                                                             -- nullable, ISO 8601 UTC; server-side task expiry -- "weather", not authoritative (spec §6.3)
    status            TEXT    NOT NULL CHECK(status IN ('working', 'input_required', 'complete', 'failed', 'cancelled', 'expired')),
    result            TEXT,                                                             -- nullable, JSON blob; terminal task result
    created_at        TEXT    NOT NULL,                                                 -- ISO 8601 UTC
    updated_at        TEXT    NOT NULL,                                                 -- ISO 8601 UTC
    UNIQUE(server_id, task_id)
);
CREATE INDEX idx_mcp_tasks_run_id ON mcp_tasks(run_id);
CREATE INDEX idx_mcp_tasks_status ON mcp_tasks(status);

-- ---------------------------------------------------------------------------
-- Container substrate desired state (ADR-056, mcp-realignment-spec.md §7)
--
-- The reconciler is level-triggered: these tables ARE the desired state, and
-- every pass lists the real containers by label, diffs them against these
-- rows, and converges one step. Nothing here records progress through an
-- imperative sequence, because there is no sequence to resume — a crash
-- mid-rotation is just another observed state the next pass converges from.
--
-- plugin_containers holds one desired-state row per plugin instance.
-- plugin_container_generations holds the rotation history, one row per
-- generation, each with its own instance token (start-new → health-gate →
-- switch → drain → stop). plugin_container_subnets records the per-instance
-- /24 carved out of the configurable base pool — east-west isolation is one
-- dedicated internal network per instance, so subnets are a finite resource
-- that must never be double-allocated. plugin_container_images accounts for
-- loaded OCI images so GC can tell which digests no generation still needs.
-- ---------------------------------------------------------------------------

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
);
