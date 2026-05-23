// Package policy — tool-prefix reference scanning.
//
// ScanPolicyToolRefs finds policies whose capabilities reference tools from
// a given plugin instance (by "<instance>." prefix) or whose trigger
// references the instance as a subscribed-trigger source. This is the
// safeguard that prevents uninstalling a plugin while policies still depend
// on it.
//
// Performance: O(policy_count) full-table scan + one YAML unmarshal per policy.
// Acceptable for an operator-driven delete flow; deferred to a denormalized
// join table if this path becomes hot.
package policy

import (
	"context"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// ListPoliciesQuerier is the narrow DB interface ScanPolicyToolRefs needs.
// Using an interface (not *db.Queries directly) keeps the function testable
// with a fake querier — any struct whose ListPolicies method signature matches
// satisfies it. *db.Queries satisfies this interface implicitly.
type ListPoliciesQuerier interface {
	ListPolicies(ctx context.Context) ([]db.Policy, error)
}

// ToolPolicyRef is a policy that references at least one of the given tool prefixes.
type ToolPolicyRef struct {
	ID    string
	Name  string
	Tools []string // the matching tool names from capabilities.tools
}

// policyToolRefYAML is the minimal YAML shape we unmarshal to detect tool and
// subscribed-trigger references without paying the cost of a full policy parse.
type policyToolRefYAML struct {
	Trigger struct {
		Type   string `yaml:"type"`
		Source string `yaml:"source"`
	} `yaml:"trigger"`
	Capabilities struct {
		Tools []struct {
			Tool string `yaml:"tool"`
		} `yaml:"tools"`
	} `yaml:"capabilities"`
}

// ScanPolicyToolRefs iterates all policies returned by q and returns those that
// reference tools with any of the given prefixes (e.g. "slack-prod.") in their
// capabilities.tools list, or that use a "subscribed" trigger whose source
// matches one of the prefixes (prefix stripped of its trailing dot).
//
// Malformed YAML is silently skipped — a corrupt stored policy must not block
// an admin-level delete. Returns an empty slice (not nil) when no policies match.
func ScanPolicyToolRefs(ctx context.Context, q ListPoliciesQuerier, prefixes []string) ([]ToolPolicyRef, error) {
	policies, err := q.ListPolicies(ctx)
	if err != nil {
		return nil, fmt.Errorf("scan tool refs: list policies: %w", err)
	}

	var refs []ToolPolicyRef
	seen := make(map[string]bool)

	for _, p := range policies {
		var raw policyToolRefYAML
		if err := yaml.Unmarshal([]byte(p.Yaml), &raw); err != nil {
			// Malformed YAML in a stored policy should not block the caller.
			continue
		}

		var matchedTools []string

		// Check capabilities.tools for any prefix match.
		for _, t := range raw.Capabilities.Tools {
			for _, prefix := range prefixes {
				if strings.HasPrefix(t.Tool, prefix) {
					matchedTools = append(matchedTools, t.Tool)
					break
				}
			}
		}

		// Check the subscribed trigger source. The source field is the bare
		// instance name (no trailing dot), so strip the trailing dot from
		// each prefix before comparing.
		if raw.Trigger.Type == "subscribed" {
			for _, prefix := range prefixes {
				instanceName := strings.TrimSuffix(prefix, ".")
				if raw.Trigger.Source == instanceName {
					// Record subscribed trigger match even with no tool refs.
					matchedTools = append(matchedTools, "[subscribed trigger]")
					break
				}
			}
		}

		if len(matchedTools) > 0 && !seen[p.ID] {
			seen[p.ID] = true
			refs = append(refs, ToolPolicyRef{
				ID:    p.ID,
				Name:  p.Name,
				Tools: matchedTools,
			})
		}
	}

	if refs == nil {
		refs = []ToolPolicyRef{}
	}
	return refs, nil
}

// ScanPolicyToolRefsForInstance is a convenience wrapper that scans for a
// single instance: it builds the prefix "<instanceName>." and delegates to
// ScanPolicyToolRefs.
func ScanPolicyToolRefsForInstance(ctx context.Context, q ListPoliciesQuerier, instanceName string) ([]ToolPolicyRef, error) {
	return ScanPolicyToolRefs(ctx, q, []string{instanceName + "."})
}
