package audience

import (
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
)

// ProposedEntry is the caller-supplied description of a single audience entry
// being saved. It carries only the fields relevant to coverage validation.
type ProposedEntry struct {
	PluginInstanceID string
	Notify           bool
	Request          bool
}

// ValidateAudienceCoverage checks that the proposed audience configuration
// satisfies the in-app fallback rules.
//
// disable=false is always valid — the synthetic in-app entry covers Request.
//
// disable=true requires at least one entry with Request==true whose plugin
// manifest declares ImplementsRequest. instanceCanRequest is called once per
// entry that has Request==true; it must return (true, nil) for at least one of
// them. A callback error is propagated as a Go error (not a FieldError).
func ValidateAudienceCoverage(
	entries []ProposedEntry,
	disableInAppFallback bool,
	instanceCanRequest func(instanceID string) (bool, error),
) ([]configvalidate.FieldError, error) {
	if !disableInAppFallback {
		return nil, nil
	}

	// Fallback is disabled: verify ≥1 Request-capable entry exists.
	for _, e := range entries {
		if !e.Request {
			continue
		}
		ok, err := instanceCanRequest(e.PluginInstanceID)
		if err != nil {
			return nil, err
		}
		if ok {
			return nil, nil
		}
	}

	return []configvalidate.FieldError{{
		Field:   "disable_in_app_fallback",
		Message: "at least one Request-capable audience entry is required when in-app fallback is disabled",
	}}, nil
}
