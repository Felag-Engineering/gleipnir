package caphealth

import "github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"

// AttestedEventKindsFromManifest extracts the event-kind identifiers a v2
// manifest's `gleipnir.event_kinds` block attests, for populating
// Target.AttestedEventKinds.
//
// It is a standalone mapping helper rather than a method on a production
// TargetLister because no production TargetLister exists yet -- the prober
// is not wired into main.go (root CLAUDE.md's MCP realignment note: the
// container substrate is built but not live). It exists so that future
// wiring reads AttestedEventKinds from the manifest in ONE place, rather
// than every call site re-deriving "the Kind field of every EventKindDecl"
// by hand and risking a different answer each time.
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
