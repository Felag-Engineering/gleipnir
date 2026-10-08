// diff_v2_fields.go holds the DiffV2 comparisons for the capability-bearing
// v2 manifest fields beyond event kinds, auth and tier-2 (#1035), plus the
// field-coverage table that keeps future fields from being silently missed.
package manifest

import (
	"bytes"
	"fmt"

	"github.com/felag-engineering/gleipnir/internal/plugin/resources"
	"github.com/felag-engineering/gleipnir/internal/plugin/schemautil"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
	"gopkg.in/yaml.v3"
)

// v2FieldCoverage classifies every field of the v2 manifest types for the
// material-change block. Keys are "Type.Field"; the value says how DiffV2
// handles the field or why it needs no diff.
// TestDiffV2ClassifiesEveryManifestField walks the manifest types by
// reflection and fails on any field missing here, so adding a field forces a
// decision: diff it, or write down why not.
var v2FieldCoverage = map[string]string{
	"Manifest.SchemaVersion": "not diffed: Validate pins it to the single accepted value",
	"Manifest.Name":          "material (diffIdentityV2): a different name is a different plugin identity",
	"Manifest.Version":       "cosmetic (diffIdentityV2): display string, same as v1",
	"Manifest.Description":   "cosmetic (diffIdentityV2): consent-screen text, same as v1",
	"Manifest.Repository":    "not diffed: informational link, never fetched",
	"Manifest.Package":       "see Package.*",
	"Manifest.Gleipnir":      "see Gleipnir.*",

	"Repository.URL":    "not diffed: informational, Gleipnir never fetches it",
	"Repository.Source": "not diffed: informational",

	// The image is signed as part of the bundle (the signature covers archive and
	// manifest, so an image from a different key is caught by the TOFU pubkey
	// check) and the loader refuses a bundle whose loaded image differs from
	// this pin (ErrImageDigestMismatch) before any row is written. A new image
	// under the already-trusted key with unchanged grants is the same trust
	// decision as a v1 binary update, which v1 also treats as cosmetic. Any
	// widened grant is caught by the other fields.
	"Package.RegistryType": "not diffed: Validate pins it to oci",
	"Package.Identifier":   "cosmetic (diffIdentityV2): integrity guarded by bundle signature + loader digest verification, not by consent",
	"Package.Version":      "cosmetic (diffIdentityV2): display string",
	"Package.Transport":    "see Transport.*",

	"Transport.Type": "not diffed: Validate pins it to streamable-http",
	"Transport.Port": "not diffed: routing detail inside the instance's own network, grants nothing",

	"Gleipnir.Profiles":          "see Profiles.* (diffProfilesV2, diffEventSourceProfileV2)",
	"Gleipnir.Egress":            "material when a domain is added (diffEgressV2); removal and reason edits are cosmetic",
	"Gleipnir.Resources":         "material when the effective cpu or memory limit rises (diffResourcesV2)",
	"Gleipnir.Auth":              "material (diffAuthV2)",
	"Gleipnir.Tier2Capabilities": "material (diffTier2V2)",
	"Gleipnir.Tools":             "material when an elicitation_kind changes (diffToolsV2)",
	"Gleipnir.EventKinds":        "material (diffEventKindsV2)",
	"Gleipnir.ConfigSchema":      "material (diffSchemasV2): stored instance config may stop validating",
	"Gleipnir.UserConfigSchema":  "material (diffSchemasV2): stored user config may stop validating",
	"Gleipnir.SBOM":              "not diffed: badge path, never parsed",

	"Profiles.ToolProvider":     "not diffed: baseline profile with no fields, grants nothing beyond what every plugin has",
	"Profiles.EventSource":      "material when added (diffProfilesV2); schema in EventSourceProfile.*",
	"Profiles.HumanChannel":     "material when added (diffProfilesV2); assurance in HumanChannelProfile.*",
	"Profiles.IdentityProvider": "material when added (diffProfilesV2); methods in IdentityProviderProfile.*",

	"EventSourceProfile.SubscriptionSchema": "material (diffEventSourceProfileV2)",
	"HumanChannelProfile.Assurance":         "material (diffProfilesV2): decides which request kinds the channel may settle",
	"IdentityProviderProfile.LinkMethods":   "material when a method is added (diffProfilesV2)",

	"EgressGrant.Domain": "see Gleipnir.Egress",
	"EgressGrant.Reason": "cosmetic (diffEgressV2): consent-screen text",

	"Resources.MemoryMB":      "see Gleipnir.Resources",
	"Resources.CPUMillicores": "see Gleipnir.Resources",

	"AuthDecl.Strategy":      "material (diffAuthV2)",
	"AuthDecl.HeaderName":    "material (diffAuthV2)",
	"AuthDecl.HeaderNames":   "material (diffAuthV2)",
	"AuthDecl.OAuthDefaults": "material (diffOAuthDefaultsV2)",

	"OAuthDefaultsDecl.AuthorizationURL": "material (diffOAuthDefaultsV2)",
	"OAuthDefaultsDecl.TokenURL":         "material (diffOAuthDefaultsV2)",
	"OAuthDefaultsDecl.Scopes":           "material (diffOAuthDefaultsV2)",

	"ToolDecl.Name":            "see Gleipnir.Tools",
	"ToolDecl.ElicitationKind": "material (diffToolsV2): decides who may answer and over which channels",

	"EventKindDecl.Kind":          "material (diffEventKindsV2)",
	"EventKindDecl.Description":   "cosmetic (diffEventKindsV2)",
	"EventKindDecl.Guidance":      "cosmetic (diffEventKindsV2)",
	"EventKindDecl.BindingSchema": "material (diffEventKindsV2)",
	"EventKindDecl.Operators":     "material (diffEventKindsV2)",
}

// effectiveResourcesV2 resolves what a manifest's resources block actually
// enforces. An absent or zero field means the HOST DEFAULT applies, not "no
// limit" (see resources.Resolve), so comparing effective values is the only
// comparison that also notices a move between a declared limit and a default
// that happens to be higher.
func effectiveResourcesV2(m *manifestv2.Manifest) (resources.Effective, error) {
	var declared resources.Limits
	if r := m.Gleipnir.Resources; r != nil {
		var err error
		declared, err = resources.FromManifestMiB(r.MemoryMB, r.CPUMillicores)
		if err != nil {
			return resources.Effective{}, err
		}
	}
	return resources.Resolve(declared, resources.Limits{}), nil
}

// diffResourcesV2 reports a material change when the effective CPU or memory
// limit rises, including declared -> absent when the host default is higher;
// a decrease is cosmetic. A block that cannot be resolved is material: the
// diff cannot show it does not widen anything, so it fails closed.
func diffResourcesV2(old, new *manifestv2.Manifest) []Change {
	oldEff, oldErr := effectiveResourcesV2(old)
	newEff, newErr := effectiveResourcesV2(new)
	if oldErr != nil || newErr != nil {
		return []Change{{Field: "resources", Material: true, From: describeResourcesV2(old), To: describeResourcesV2(new)}}
	}

	var changes []Change
	if oldEff.MemoryBytes != newEff.MemoryBytes {
		changes = append(changes, Change{
			Field:    "resources.memory_mb",
			Material: newEff.MemoryBytes > oldEff.MemoryBytes,
			From:     fmt.Sprintf("%d", oldEff.MemoryBytes>>20),
			To:       fmt.Sprintf("%d", newEff.MemoryBytes>>20),
		})
	}
	if oldEff.CPUMillicores != newEff.CPUMillicores {
		changes = append(changes, Change{
			Field:    "resources.cpu_millicores",
			Material: newEff.CPUMillicores > oldEff.CPUMillicores,
			From:     fmt.Sprintf("%d", oldEff.CPUMillicores),
			To:       fmt.Sprintf("%d", newEff.CPUMillicores),
		})
	}
	return changes
}

func describeResourcesV2(m *manifestv2.Manifest) string {
	r := m.Gleipnir.Resources
	if r == nil {
		return "default"
	}
	return fmt.Sprintf("memory_mb=%d cpu_millicores=%d", r.MemoryMB, r.CPUMillicores)
}

// diffEgressV2 compares egress grants keyed by domain. A new domain widens
// what the container may reach and is material; a removed domain narrows it
// and a reason edit is consent-screen text, both cosmetic.
func diffEgressV2(old, new *manifestv2.Manifest) []Change {
	oldMap := make(map[string]string, len(old.Gleipnir.Egress))
	for _, g := range old.Gleipnir.Egress {
		oldMap[g.Domain] = g.Reason
	}
	newMap := make(map[string]string, len(new.Gleipnir.Egress))
	for _, g := range new.Gleipnir.Egress {
		newMap[g.Domain] = g.Reason
	}

	var changes []Change
	for domain, reason := range newMap {
		oldReason, ok := oldMap[domain]
		switch {
		case !ok:
			changes = append(changes, Change{Field: "egress." + domain, Material: true, From: "", To: domain})
		case oldReason != reason:
			changes = append(changes, Change{Field: "egress." + domain + ".reason", Material: false, From: oldReason, To: reason})
		}
	}
	for domain := range oldMap {
		if _, ok := newMap[domain]; !ok {
			changes = append(changes, Change{Field: "egress." + domain, Material: false, From: domain, To: ""})
		}
	}
	return changes
}

// diffToolsV2 compares per-tool elicitation_kind declarations keyed by tool
// name. The kind decides which role may answer a tool's question and which
// channels may settle it, so any change — including adding or dropping a
// declaration, which swaps the declared kind for the runtime convention — is
// material.
func diffToolsV2(old, new *manifestv2.Manifest) []Change {
	oldKinds := toolKindsV2(old.Gleipnir.Tools)
	newKinds := toolKindsV2(new.Gleipnir.Tools)

	var changes []Change
	for name, kind := range newKinds {
		if oldKind, ok := oldKinds[name]; !ok || oldKind != kind {
			changes = append(changes, Change{Field: "tools." + name + ".elicitation_kind", Material: true, From: oldKinds[name], To: kind})
		}
	}
	for name, kind := range oldKinds {
		if _, ok := newKinds[name]; !ok {
			changes = append(changes, Change{Field: "tools." + name + ".elicitation_kind", Material: true, From: kind, To: ""})
		}
	}
	return changes
}

func toolKindsV2(tools []manifestv2.ToolDecl) map[string]string {
	m := make(map[string]string, len(tools))
	for _, t := range tools {
		// A declaration with no kind says nothing; treat it as absent so that
		// listing a bare tool name is not a change.
		if t.ElicitationKind != "" {
			m[t.Name] = t.ElicitationKind
		}
	}
	return m
}

// diffProfilesV2 reports profile additions and capability-bearing profile
// fields. A newly declared profile lights up a host surface the admin never
// reviewed, so it is material; removing one narrows the surface and is
// cosmetic. event_source's subscription_schema is diffEventSourceProfileV2's.
func diffProfilesV2(old, new *manifestv2.Manifest) []Change {
	op, np := old.Gleipnir.Profiles, new.Gleipnir.Profiles

	var changes []Change
	if (op.EventSource == nil) != (np.EventSource == nil) {
		changes = append(changes, Change{Field: "profiles.event_source", Material: np.EventSource != nil, From: presenceV2(op.EventSource != nil), To: presenceV2(np.EventSource != nil)})
	}

	switch {
	case op.HumanChannel == nil && np.HumanChannel != nil:
		changes = append(changes, Change{Field: "profiles.human_channel", Material: true, From: "absent", To: "present"})
	case op.HumanChannel != nil && np.HumanChannel == nil:
		changes = append(changes, Change{Field: "profiles.human_channel", Material: false, From: "present", To: "absent"})
	case op.HumanChannel != nil && op.HumanChannel.Assurance != np.HumanChannel.Assurance:
		changes = append(changes, Change{Field: "profiles.human_channel.assurance", Material: true, From: op.HumanChannel.Assurance, To: np.HumanChannel.Assurance})
	}

	var oldMethods, newMethods []string
	if op.IdentityProvider != nil {
		oldMethods = op.IdentityProvider.LinkMethods
	}
	if np.IdentityProvider != nil {
		newMethods = np.IdentityProvider.LinkMethods
	}
	presenceChanged := (op.IdentityProvider == nil) != (np.IdentityProvider == nil)
	if presenceChanged || sortedJoin(oldMethods) != sortedJoin(newMethods) {
		added := np.IdentityProvider != nil && (op.IdentityProvider == nil || hasAddedString(oldMethods, newMethods))
		changes = append(changes, Change{Field: "profiles.identity_provider", Material: added, From: sortedJoin(oldMethods), To: sortedJoin(newMethods)})
	}
	return changes
}

func presenceV2(present bool) string {
	if present {
		return "present"
	}
	return "absent"
}

// hasAddedString reports whether next contains a value absent from prev.
func hasAddedString(prev, next []string) bool {
	seen := make(map[string]bool, len(prev))
	for _, s := range prev {
		seen[s] = true
	}
	for _, s := range next {
		if !seen[s] {
			return true
		}
	}
	return false
}

// diffSchemasV2 compares the instance and user config schemas after stripping
// cosmetic keys. Material for the same reason as v1's config_schema: stored
// config might no longer validate against the new shape.
func diffSchemasV2(old, new *manifestv2.Manifest) []Change {
	schemas := []struct {
		field    string
		old, new *yaml.Node
	}{
		{"config_schema", old.Gleipnir.ConfigSchema, new.Gleipnir.ConfigSchema},
		{"user_config_schema", old.Gleipnir.UserConfigSchema, new.Gleipnir.UserConfigSchema},
	}
	var changes []Change
	for _, s := range schemas {
		oldBytes := schemautil.ToJSONStripped(s.old)
		newBytes := schemautil.ToJSONStripped(s.new)
		if !bytes.Equal(oldBytes, newBytes) {
			changes = append(changes, Change{Field: s.field, Material: true, From: string(oldBytes), To: string(newBytes)})
		}
	}
	return changes
}

// diffIdentityV2 covers the top-level identity and display fields. A name
// change is a different plugin and is material; the rest mirror v1's cosmetic
// version/description handling. The image identifier is cosmetic here because
// its integrity is enforced by the signature and the loader's digest check
// (see v2FieldCoverage), not by admin re-consent.
func diffIdentityV2(old, new *manifestv2.Manifest) []Change {
	var changes []Change
	if old.Name != new.Name {
		changes = append(changes, Change{Field: "name", Material: true, From: old.Name, To: new.Name})
	}
	if old.Version != new.Version {
		changes = append(changes, Change{Field: "version", Material: false, From: old.Version, To: new.Version})
	}
	if old.Description != new.Description {
		changes = append(changes, Change{Field: "description", Material: false, From: old.Description, To: new.Description})
	}
	if old.Package.Identifier != new.Package.Identifier {
		changes = append(changes, Change{Field: "package.identifier", Material: false, From: old.Package.Identifier, To: new.Package.Identifier})
	}
	if old.Package.Version != new.Package.Version {
		changes = append(changes, Change{Field: "package.version", Material: false, From: old.Package.Version, To: new.Package.Version})
	}
	return changes
}
