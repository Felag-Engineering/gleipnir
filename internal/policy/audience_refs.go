// Package policy — audience reference scanning.
//
// Agreed YAML key: `audience` at policy root (string, name of the referenced
// audience). schemas/policy.yaml does NOT yet define this key; schema update
// is owned by #208/#210. Until that key is written and the policy editor
// emits it, ScanPolicyReferences and BulkScanPolicyReferenceCounts always
// return empty results — no policies will reference an audience by name yet.
//
// Performance note: both functions do an O(policy_count) full-table scan plus
// one YAML unmarshal per policy. For v1 this is acceptable because audiences
// are admin-only resources mutated infrequently. If this path becomes hot, the
// v2 fix is a denormalized policy_audience_refs join table (deferred per
// issue #290).
package policy

import (
	"context"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// PolicyRef is a lightweight policy identity used in audience reference results.
type PolicyRef struct {
	ID   string
	Name string
}

// policyAudienceYAML is the minimal struct we unmarshal just to read the
// top-level `audience` key without paying the cost of a full policy parse.
type policyAudienceYAML struct {
	Audience string `yaml:"audience"`
}

// ScanPolicyReferences returns all policies that reference audienceName in
// their `audience` field. Returns an empty slice (not nil) when none match.
func ScanPolicyReferences(ctx context.Context, q *db.Queries, audienceName string) ([]PolicyRef, error) {
	policies, err := q.ListPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("scan audience refs: list policies: %w", err)
	}

	var refs []PolicyRef
	for _, p := range policies {
		var raw policyAudienceYAML
		if err := yaml.Unmarshal([]byte(p.Yaml), &raw); err != nil {
			// Malformed YAML in a stored policy should not block the caller.
			continue
		}
		if raw.Audience == audienceName {
			refs = append(refs, PolicyRef{ID: p.ID, Name: p.Name})
		}
	}
	if refs == nil {
		refs = []PolicyRef{}
	}
	return refs, nil
}

// BulkScanPolicyReferenceCounts returns a map from audience name to the count
// of policies that reference it. Only audience names that appear at least once
// are present in the map.
func BulkScanPolicyReferenceCounts(ctx context.Context, q *db.Queries) (map[string]int, error) {
	policies, err := q.ListPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("bulk scan audience ref counts: list policies: %w", err)
	}

	counts := make(map[string]int)
	for _, p := range policies {
		var raw policyAudienceYAML
		if err := yaml.Unmarshal([]byte(p.Yaml), &raw); err != nil {
			continue
		}
		if raw.Audience != "" {
			counts[raw.Audience]++
		}
	}
	return counts, nil
}
