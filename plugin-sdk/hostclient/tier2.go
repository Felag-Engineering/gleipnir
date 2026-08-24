package hostclient

import "context"

// The two Tier-2 methods (spec §8.2): capability-gated by the plugin's own
// manifest tier2_capabilities declaration. A call from an instance whose
// manifest does not declare the matching capability returns a
// permission_denied HostError — there is no argument that unlocks it, only
// the manifest.

// RunHistoryReadRequest is host/run_history_read's arguments. Both fields
// are optional: an empty PolicyID returns runs across every policy the
// calling instance is scoped to, and Limit defaults to (and caps at) 100.
type RunHistoryReadRequest struct {
	PolicyID string `json:"policy_id,omitempty"`
	Limit    int64  `json:"limit,omitempty"`
}

// RunSummary is one entry of host/run_history_read's result.
type RunSummary struct {
	RunID      string `json:"run_id"`
	PolicyID   string `json:"policy_id"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
}

// RunHistoryReadResponse is host/run_history_read's result.
type RunHistoryReadResponse struct {
	Runs []RunSummary `json:"runs"`
}

// RunHistoryRead returns past runs for policies that grant the calling
// instance a tool (or bind a subscribed trigger to it) — scoped to what the
// instance's own grants reach, never every run on the host. Requires the
// run_history_read Tier-2 capability.
func (c *Client) RunHistoryRead(ctx context.Context, req RunHistoryReadRequest) (*RunHistoryReadResponse, error) {
	var out RunHistoryReadResponse
	if err := c.callTool(ctx, toolRunHistoryRead, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UserDirectoryReadRequest is host/user_directory_read's arguments.
// RoleFilter may be empty to return every active user across all roles, or
// one of admin/operator/approver/auditor to narrow to that role.
type UserDirectoryReadRequest struct {
	RoleFilter string `json:"role_filter,omitempty"`
}

// UserEntry is one entry of host/user_directory_read's result — exactly
// (user_id, username, role), never credentials or session data.
type UserEntry struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// UserDirectoryReadResponse is host/user_directory_read's result.
type UserDirectoryReadResponse struct {
	Users []UserEntry `json:"users"`
}

// UserDirectoryRead returns (user_id, username, role) tuples for active
// users. Requires the user_directory_read Tier-2 capability.
func (c *Client) UserDirectoryRead(ctx context.Context, req UserDirectoryReadRequest) (*UserDirectoryReadResponse, error) {
	var out UserDirectoryReadResponse
	if err := c.callTool(ctx, toolUserDirectoryRead, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
