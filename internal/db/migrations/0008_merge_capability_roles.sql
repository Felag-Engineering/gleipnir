-- Migration 0008: Merge sensor/actuator capability roles into "tool"
--
-- The sensor/actuator distinction was Gleipnir-imposed metadata that doesn't
-- align with MCP's tool model. All tools are now classified as either "tool"
-- (callable by the agent, optionally approval-gated) or "feedback" (human-in-the-loop).
--
-- SQLite cannot ALTER CHECK constraints, so we rebuild the table.

CREATE TABLE mcp_tools_new (
    id              TEXT    PRIMARY KEY,
    server_id       TEXT    NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE,
    name            TEXT    NOT NULL,
    description     TEXT    NOT NULL,
    input_schema    TEXT    NOT NULL,
    capability_role TEXT    NOT NULL CHECK(capability_role IN ('tool', 'feedback')),
    created_at      TEXT    NOT NULL,
    UNIQUE(server_id, name)
);

INSERT INTO mcp_tools_new (id, server_id, name, description, input_schema, capability_role, created_at)
SELECT id, server_id, name, description, input_schema,
    CASE WHEN capability_role IN ('sensor', 'actuator') THEN 'tool' ELSE capability_role END,
    created_at
FROM mcp_tools;

DROP TABLE mcp_tools;
ALTER TABLE mcp_tools_new RENAME TO mcp_tools;

CREATE INDEX idx_mcp_tools_server_id ON mcp_tools(server_id);

INSERT INTO schema_migrations(version, applied_at)
VALUES (8, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));
