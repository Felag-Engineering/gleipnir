package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// AddMCPServerCACert adds mcp_servers.ca_cert_pem, an optional per-server CA
// certificate (PEM) pinned as that server's sole TLS trust root (issue #928).
//
// This is plaintext, unlike the ADR-039 auth-header columns. A CA certificate
// is a public object — the whole point of pinning it is that an operator can
// come back later and confirm exactly which CA they trusted. The ADR-039
// encrypted/write-only/redact-on-read shape exists to protect secrets; applying
// it here would be security theater over public bytes, and would make the pin
// impossible to verify. Client certificates for mTLS *to* an MCP server are out
// of scope (they are a real secret and would need that treatment) and are
// deferred to a later, additive column.
type AddMCPServerCACert struct{}

func (m *AddMCPServerCACert) Version() int { return 42 }
func (m *AddMCPServerCACert) Name() string { return "add_mcp_server_ca_cert" }

func (m *AddMCPServerCACert) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(mcp_servers)`)
	if err != nil {
		return false, fmt.Errorf("inspect mcp_servers columns: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			colType    string
			notNull    int
			dfltValue  sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &primaryKey); err != nil {
			return false, fmt.Errorf("scan mcp_servers column: %w", err)
		}
		if name == "ca_cert_pem" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate mcp_servers columns: %w", err)
	}
	return false, nil
}

func (m *AddMCPServerCACert) Up(ctx context.Context, tx *sql.Tx) error {
	const ddl = `ALTER TABLE mcp_servers ADD COLUMN ca_cert_pem TEXT;`

	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("add mcp_servers.ca_cert_pem: %w", err)
	}

	slog.Info("migrated: added ca_cert_pem to mcp_servers")
	return nil
}
