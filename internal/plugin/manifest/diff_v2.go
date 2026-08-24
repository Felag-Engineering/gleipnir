// diff_v2.go diffs plugin-sdk/manifestv2 manifests (ADR-053/ADR-054), material
// vs cosmetic per the same rules diff.go applies to v1. It is a SEPARATE entry
// point rather than a Diff overload: v1's sdkmanifest and v2's manifestv2 are
// different Go types describing different substrates, and this only covers
// the slice of v2's surface the material-change hot-reload block (ADR-045)
// needs from the event-source profile today — event_kinds and the profile's
// subscription_schema. Widen it as more of v2 gets wired to hot-reload.
package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/felag-engineering/gleipnir/internal/plugin/schemautil"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
	"gopkg.in/yaml.v3"
)

// DiffV2 returns the list of differences between old and new v2 manifests,
// following the same material-vs-cosmetic split Diff applies to v1.
func DiffV2(old, new *manifestv2.Manifest) []Change {
	var changes []Change
	changes = append(changes, diffEventKindsV2(old, new)...)
	changes = append(changes, diffEventSourceProfileV2(old, new)...)
	return changes
}

// diffEventKindsV2 compares event_kinds keyed by Kind. Added/removed kinds
// are material, as is a BindingSchema or Operators change on an existing kind
// — Operators widens or narrows what a policy binding may express, exactly
// what the hot-reload block exists to catch (ADR-052). Description and
// Guidance are cosmetic: both are operator-facing help text with no
// enforcement effect.
func diffEventKindsV2(old, new *manifestv2.Manifest) []Change {
	oldMap := eventKindMapV2(old.Gleipnir.EventKinds)
	newMap := eventKindMapV2(new.Gleipnir.EventKinds)

	var changes []Change
	for kind := range oldMap {
		if _, ok := newMap[kind]; !ok {
			changes = append(changes, Change{Field: "event_kinds." + kind, Material: true, From: kind, To: ""})
		}
	}
	for kind := range newMap {
		if _, ok := oldMap[kind]; !ok {
			changes = append(changes, Change{Field: "event_kinds." + kind, Material: true, From: "", To: kind})
		}
	}
	for kind, oldEK := range oldMap {
		newEK, ok := newMap[kind]
		if !ok {
			continue
		}

		oldBinding := schemautil.ToJSONStripped(oldEK.BindingSchema)
		newBinding := schemautil.ToJSONStripped(newEK.BindingSchema)
		if !bytes.Equal(oldBinding, newBinding) {
			changes = append(changes, Change{Field: "event_kinds." + kind + ".binding_schema", Material: true, From: string(oldBinding), To: string(newBinding)})
		}

		oldOps := canonicalOperators(oldEK.Operators)
		newOps := canonicalOperators(newEK.Operators)
		if oldOps != newOps {
			changes = append(changes, Change{Field: "event_kinds." + kind + ".operators", Material: true, From: oldOps, To: newOps})
		}

		if oldEK.Description != newEK.Description {
			changes = append(changes, Change{Field: "event_kinds." + kind + ".description", Material: false, From: oldEK.Description, To: newEK.Description})
		}
		if oldEK.Guidance != newEK.Guidance {
			changes = append(changes, Change{Field: "event_kinds." + kind + ".guidance", Material: false, From: oldEK.Guidance, To: newEK.Guidance})
		}
	}
	return changes
}

func eventKindMapV2(kinds []manifestv2.EventKindDecl) map[string]manifestv2.EventKindDecl {
	m := make(map[string]manifestv2.EventKindDecl, len(kinds))
	for _, ek := range kinds {
		m[ek.Kind] = ek
	}
	return m
}

// canonicalOperators renders an operators map deterministically for
// comparison and display: each field's allowed-operator list is sorted (the
// list is a set, and declaration order carries no meaning), then the whole
// map is JSON-marshalled — Go's json package emits map keys alphabetically,
// so two maps with the same content always render identically regardless of
// field order.
func canonicalOperators(m map[string][]string) string {
	if len(m) == 0 {
		return ""
	}
	canon := make(map[string][]string, len(m))
	for field, ops := range m {
		cp := make([]string, len(ops))
		copy(cp, ops)
		sort.Strings(cp)
		canon[field] = cp
	}
	b, err := json.Marshal(canon)
	if err != nil {
		// json.Marshal(map[string][]string) is effectively infallible, but if
		// it somehow fails return a deterministic fallback rather than an
		// empty string, which would read as "no operators declared".
		return fmt.Sprintf("%v", canon)
	}
	return string(b)
}

// diffEventSourceProfileV2 compares the event_source profile's
// subscription_schema. Material, same rationale as config_schema: a stored
// subscription_scope_json might no longer validate against a changed schema.
// The same cosmetic-key stripping applies as for config_schema — a schema
// author annotating a field's description or default should not block a
// hot-reload that changes nothing enforceable.
func diffEventSourceProfileV2(old, new *manifestv2.Manifest) []Change {
	oldBytes := schemautil.ToJSONStripped(eventSourceSubscriptionSchema(old))
	newBytes := schemautil.ToJSONStripped(eventSourceSubscriptionSchema(new))
	if bytes.Equal(oldBytes, newBytes) {
		return nil
	}
	return []Change{{Field: "profiles.event_source.subscription_schema", Material: true, From: string(oldBytes), To: string(newBytes)}}
}

func eventSourceSubscriptionSchema(m *manifestv2.Manifest) *yaml.Node {
	if m.Gleipnir.Profiles.EventSource == nil {
		return nil
	}
	return m.Gleipnir.Profiles.EventSource.SubscriptionSchema
}
