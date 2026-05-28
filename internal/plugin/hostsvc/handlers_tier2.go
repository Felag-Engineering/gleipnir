package hostsvc

import (
	"context"
	"sort"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/internal/db"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// hasTier2Capability checks whether the plugin manifest for inst's parent
// plugin declares the given Tier-2 capability. It reads manifest_snapshot fresh
// per call — no caching — so hot-reload invalidation (spec §5.4) is automatic.
//
// Returns (false, Internal) on manifest parse error.
func (s *Server) hasTier2Capability(ctx context.Context, inst db.PluginInstance, capability string) (bool, error) {
	plugin, err := s.q.GetPluginByID(ctx, inst.PluginID)
	if err != nil {
		return false, status.Errorf(codes.Internal, "fetch plugin: %v", err)
	}

	var m sdkmanifest.Manifest
	if err := yaml.Unmarshal([]byte(plugin.ManifestSnapshot), &m); err != nil {
		return false, status.Errorf(codes.Internal, "parse manifest snapshot: %v", err)
	}

	return m.HasTier2(capability), nil
}

// RunHistoryRead returns past runs for policies associated with the calling
// plugin instance. Requires the "run_history_read" Tier-2 capability declared
// in the plugin manifest (spec §8.2).
func (s *Server) RunHistoryRead(ctx context.Context, req *hostv1.RunHistoryReadRequest) (*hostv1.RunHistoryReadResponse, error) {
	inst, err := s.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}

	const rpcMethod = "/gleipnir.plugin.host.v1.HostService/RunHistoryRead"

	hasCap, err := s.hasTier2Capability(ctx, inst, sdkmanifest.Tier2RunHistoryRead)
	if err != nil {
		return nil, err
	}
	if err := RejectIfTier2NotDeclared(ctx, s.q, inst.ID, sdkmanifest.Tier2RunHistoryRead, rpcMethod, hasCap); err != nil {
		return nil, err
	}

	scopedIDs, err := s.policyIDsForInstance(ctx, inst)
	if err != nil {
		return nil, err
	}

	// If the caller requested a specific policy, intersect with the scoped set.
	// Return an empty list — not an error — when the policy is not in scope so we
	// do not leak the existence of policies the instance doesn't own.
	if req.GetPolicyId() != "" {
		var filtered []string
		for _, id := range scopedIDs {
			if id == req.GetPolicyId() {
				filtered = append(filtered, id)
				break
			}
		}
		scopedIDs = filtered
	}

	// Clamp limit: default to 100 when ≤ 0, hard cap at 100.
	limit := int64(req.GetLimit())
	if limit <= 0 || limit > 100 {
		limit = 100
	}

	// Fetch per-policy rows and merge them. Because each SQL call returns rows
	// ordered by created_at DESC, we merge all slices and sort once at the end.
	// Worst case: len(scopedIDs)*100 rows in memory before truncation.
	var merged []db.ListRunsByPolicyRow
	for _, policyID := range scopedIDs {
		rows, err := s.q.ListRunsByPolicy(ctx, db.ListRunsByPolicyParams{
			PolicyID: policyID,
			Limit:    limit,
		})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "list runs for policy %s: %v", policyID, err)
		}
		merged = append(merged, rows...)
	}

	// Sort merged results by created_at DESC, matching the SQL ORDER BY.
	// RFC3339 strings sort lexicographically so string comparison is correct.
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].CreatedAt > merged[j].CreatedAt
	})

	if int64(len(merged)) > limit {
		merged = merged[:limit]
	}

	summaries := make([]*hostv1.RunSummary, 0, len(merged))
	for _, r := range merged {
		finishedAt := ""
		if r.CompletedAt != nil {
			finishedAt = *r.CompletedAt
		}
		summaries = append(summaries, &hostv1.RunSummary{
			RunId:      r.ID,
			PolicyId:   r.PolicyID,
			Status:     r.Status,
			StartedAt:  r.StartedAt,
			FinishedAt: finishedAt,
		})
	}

	return &hostv1.RunHistoryReadResponse{Runs: summaries}, nil
}

// UserDirectoryRead returns user and role information for all active users.
// Requires the "user_directory_read" Tier-2 capability declared in the plugin
// manifest (spec §8.2). Only id, username, and role are returned — no
// passwords, session tokens, or deactivation metadata.
func (s *Server) UserDirectoryRead(ctx context.Context, req *hostv1.UserDirectoryReadRequest) (*hostv1.UserDirectoryReadResponse, error) {
	inst, err := s.resolveInstance(ctx)
	if err != nil {
		return nil, err
	}

	const rpcMethod = "/gleipnir.plugin.host.v1.HostService/UserDirectoryRead"

	hasCap, err := s.hasTier2Capability(ctx, inst, sdkmanifest.Tier2UserDirectoryRead)
	if err != nil {
		return nil, err
	}
	if err := RejectIfTier2NotDeclared(ctx, s.q, inst.ID, sdkmanifest.Tier2UserDirectoryRead, rpcMethod, hasCap); err != nil {
		return nil, err
	}

	var entries []*hostv1.UserEntry

	validRoles := map[string]bool{
		"admin": true, "operator": true, "approver": true, "auditor": true,
	}
	if rf := req.GetRoleFilter(); rf != "" && !validRoles[rf] {
		return nil, status.Errorf(codes.InvalidArgument, "unknown role %q", rf)
	}

	if req.GetRoleFilter() == "" {
		rows, err := s.q.ListAllActiveUsersWithRoles(ctx)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "list users: %v", err)
		}
		for _, r := range rows {
			entries = append(entries, &hostv1.UserEntry{
				UserId:   r.UserID,
				Username: r.Username,
				Role:     r.Role,
			})
		}
	} else {
		rows, err := s.q.ListActiveUsersByRole(ctx, req.GetRoleFilter())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "list users by role: %v", err)
		}
		// ListActiveUsersByRoleRow has no Role field, so we stamp it from the
		// request (the WHERE clause already filters to this role).
		for _, r := range rows {
			entries = append(entries, &hostv1.UserEntry{
				UserId:   r.UserID,
				Username: r.Username,
				Role:     req.GetRoleFilter(),
			})
		}
	}

	return &hostv1.UserDirectoryReadResponse{Users: entries}, nil
}
