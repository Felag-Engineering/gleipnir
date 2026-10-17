package conform

import (
	"context"
	"fmt"
	"slices"
)

// profileEventSource is the manifest profile name that selects EV-GL-* checks.
const profileEventSource = "event_source"

// manifestChecks compare the signed manifest with what the running server
// serves. They belong to the Gleipnir host addendum of each extension's
// checklist rather than to the neutral checklist, because "agrees with a
// signed manifest" is a Gleipnir install concept.
var manifestChecks = []Check{
	{ID: "EV-GL-01", Profile: profileEventSource, Run: checkEventKinds},
}

// EV-GL-01: the kinds events/discover returns are exactly the manifest's
// event_kinds. The host treats drift in either direction as a capability
// fault: an attested kind not served is consent the operator gave for
// nothing, and a served kind not attested is a surface nobody signed for.
func checkEventKinds(ctx context.Context, s *Session) error {
	var res struct {
		Kinds *[]struct {
			Kind string `json:"kind"`
		} `json:"kinds"`
	}
	if err := s.client.result(ctx, "events/discover", nil, &res); err != nil {
		return err
	}
	if res.Kinds == nil {
		return fmt.Errorf("events/discover: result has no kinds array")
	}

	var served []string
	for _, k := range *res.Kinds {
		served = append(served, k.Kind)
	}
	var attested []string
	for _, k := range s.target.Manifest.Gleipnir.EventKinds {
		attested = append(attested, k.Kind)
	}

	var problems []string
	for _, k := range attested {
		if !slices.Contains(served, k) {
			problems = append(problems, fmt.Sprintf("%q is in the manifest's event_kinds but not served", k))
		}
	}
	for _, k := range served {
		if !slices.Contains(attested, k) {
			problems = append(problems, fmt.Sprintf("%q is served but not in the manifest's event_kinds", k))
		}
	}
	return joinProblems("event kinds disagree with the manifest", problems)
}
