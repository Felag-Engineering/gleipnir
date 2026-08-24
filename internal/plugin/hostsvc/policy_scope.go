package hostsvc

import (
	"context"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// scopeProbe is a minimal struct for scanning the fields out of a policy YAML
// blob that determine whether the policy references the calling instance.
// We extract only what is needed to avoid a dependency on internal/policy,
// which would create an import cycle.
type scopeProbe struct {
	Capabilities struct {
		Tools []struct {
			Tool string `yaml:"tool"`
		} `yaml:"tools"`
	} `yaml:"capabilities"`
	Trigger struct {
		Type      string `yaml:"type"`
		Source    string `yaml:"source"`
		EventKind string `yaml:"event_kind"`
	} `yaml:"trigger"`
}

// policyIDsForInstance returns the IDs of policies that reference inst via
// tool grants (capabilities.tools contains an entry with the prefix
// "<instanceName>.") OR via a subscribed trigger (trigger.type == "subscribed"
// and trigger.source == instanceName). A policy reachable through both paths
// appears exactly once.
func (s *Server) policyIDsForInstance(ctx context.Context, inst db.PluginInstance) ([]string, error) {
	policies, err := s.q.ListPolicies(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list policies: %v", err)
	}

	instanceName := inst.InstanceName
	var ids []string
	for _, pol := range policies {
		var probe scopeProbe
		if err := yaml.Unmarshal([]byte(pol.Yaml), &probe); err != nil {
			// Corrupt policy YAML is not a reason to fail the RPC — skip it.
			continue
		}

		// Use strings.Cut to match only the first dot-segment: prevents
		// empty-instance and prefix-overlap false positives.
		matched := false
		for _, t := range probe.Capabilities.Tools {
			ns, _, ok := strings.Cut(t.Tool, ".")
			if ok && ns == instanceName {
				matched = true
				break
			}
		}
		if !matched &&
			probe.Trigger.Type == string(model.TriggerTypeSubscribed) &&
			probe.Trigger.Source == inst.InstanceName {
			matched = true
		}
		if matched {
			ids = append(ids, pol.ID)
		}
	}
	return ids, nil
}
