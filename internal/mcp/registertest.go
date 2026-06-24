package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// RegisterServerForTest persists a new mcp_servers row and discovers its tools
// through the real RefreshTools path (including the #194 namespace arbiter when
// the Registry was built WithToolNamespaceArbiter). It mirrors the production
// MCPHandler.Create flow. Test-only seam; no production code reaches it.
// Returns the generated server ID.
func RegisterServerForTest(ctx context.Context, q *db.Queries, reg *Registry, name, url string) (string, error) {
	if err := ValidateServerURL(ctx, url); err != nil {
		return "", fmt.Errorf("invalid server url: %w", err)
	}
	serverID := model.NewULID()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := q.CreateMCPServer(ctx, db.CreateMCPServerParams{
		ID:        serverID,
		Name:      name,
		Url:       url,
		CreatedAt: now,
	}); err != nil {
		return "", fmt.Errorf("create mcp server: %w", err)
	}
	if _, err := reg.RefreshTools(ctx, serverID); err != nil {
		return serverID, fmt.Errorf("refresh tools: %w", err)
	}
	return serverID, nil
}
