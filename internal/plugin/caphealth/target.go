package caphealth

import "github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"

// AttestedEventKindsFromManifest extracts the event-kind identifiers a v2
// manifest's `gleipnir.event_kinds` block attests, for populating
// Target.AttestedEventKinds.
//
// It is a standalone mapping helper rather than a method on a TargetLister
// so that every producer of Targets reads AttestedEventKinds from the manifest
// in ONE place, rather than each call site re-deriving "the Kind field of
// every EventKindDecl" by hand and risking a different answer each time.
// (The prober runs only in the substratev2 build; the v2 assembly feeds it
// from the reconciler's observe pass.)
//
// Returns nil, not an empty non-nil slice, for a manifest that attests no
// event kinds -- matching DriftDetail's own treatment of "nothing attested"
// and Target.AttestedEventKinds's doc ("empty when the plugin declares no
// event source").
func AttestedEventKindsFromManifest(decls []manifestv2.EventKindDecl) []string {
	if len(decls) == 0 {
		return nil
	}
	out := make([]string, len(decls))
	for i, d := range decls {
		out[i] = d.Kind
	}
	return out
}
