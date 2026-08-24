// Package manifest provides material-vs-cosmetic diff logic for plugin manifests.
// Diff operates on parsed *manifest.Manifest values from plugin-sdk/manifest
// (the v1.1 gRPC substrate); DiffV2 (diff_v2.go) operates on
// plugin-sdk/manifestv2 (the ADR-053 realignment target). The two are
// separate entry points, not one generic function, because the underlying
// types describe different substrates and only DiffV2's slice of v2's surface
// has a hot-reload consumer today.
//
// Pubkey-claim diffing (spec §5.4 bullet 1) is intentionally absent here;
// key-mismatch detection is owned by issue #188 (internal/plugin/loader install.go,
// handlePubkeyMismatch). Only the remaining material categories are handled:
// services, tier-2 capabilities, OAuth, tools, config_schema shape, event_kinds.
package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/felag-engineering/gleipnir/internal/plugin/schemautil"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	"gopkg.in/yaml.v3"
)

// Change records a single detected difference between two manifest versions.
type Change struct {
	Field    string
	Material bool
	From     string
	To       string
}

// Diff returns the list of differences between old and new. Material differences
// block reload; cosmetic differences update the snapshot silently.
func Diff(old, new *sdkmanifest.Manifest) []Change {
	var changes []Change

	changes = append(changes, diffServices(old, new)...)
	changes = append(changes, diffTier2(old, new)...)
	changes = append(changes, diffAuth(old, new)...)
	changes = append(changes, diffTools(old, new)...)
	changes = append(changes, diffConfigSchema(old, new)...)
	changes = append(changes, diffSubscriptionSchema(old, new)...)
	changes = append(changes, diffEventKinds(old, new)...)

	// Cosmetic fields: description, version, author, license, sbom.
	if old.Description != new.Description {
		changes = append(changes, Change{Field: "description", Material: false, From: old.Description, To: new.Description})
	}
	if old.Version != new.Version {
		changes = append(changes, Change{Field: "version", Material: false, From: old.Version, To: new.Version})
	}
	if old.Author != new.Author {
		changes = append(changes, Change{Field: "author", Material: false, From: old.Author, To: new.Author})
	}
	if old.License != new.License {
		changes = append(changes, Change{Field: "license", Material: false, From: old.License, To: new.License})
	}
	if old.SBOM != new.SBOM {
		changes = append(changes, Change{Field: "sbom", Material: false, From: old.SBOM, To: new.SBOM})
	}

	return changes
}

// HasMaterial reports whether any change in the slice is material.
func HasMaterial(changes []Change) bool {
	for _, c := range changes {
		if c.Material {
			return true
		}
	}
	return false
}

// MaterialFields returns the Field values of all material changes.
func MaterialFields(changes []Change) []string {
	var out []string
	for _, c := range changes {
		if c.Material {
			out = append(out, c.Field)
		}
	}
	return out
}

// CosmeticFields returns the Field values of all cosmetic (non-material) changes.
func CosmeticFields(changes []Change) []string {
	var out []string
	for _, c := range changes {
		if !c.Material {
			out = append(out, c.Field)
		}
	}
	return out
}

// ConfigSchemaNewlyRequiredFields returns the names of JSON Schema properties
// that are required in new.ConfigSchema but absent from (or not required in)
// old.ConfigSchema. This drives the pending_config_migration transition when
// an admin accepts a material manifest change.
//
// "Newly required" means: a property name appears in new's "required" array
// but did not appear in old's "required" array. Plugin-wide granularity (not
// per-instance) per the v1 design decision.
//
// A nil ConfigSchema or a schema with no "required" key is not an error (not
// all plugins declare required config). A malformed "required" key (wrong type
// or non-string element) returns an error; callers must fail closed.
func ConfigSchemaNewlyRequiredFields(old, new *sdkmanifest.Manifest) ([]string, error) {
	oldRequired, err := requiredFieldSet(old.ConfigSchema)
	if err != nil {
		return nil, fmt.Errorf("old config schema: %w", err)
	}
	newRequired, err := requiredFieldSet(new.ConfigSchema)
	if err != nil {
		return nil, fmt.Errorf("new config schema: %w", err)
	}

	var added []string
	for field := range newRequired {
		if !oldRequired[field] {
			added = append(added, field)
		}
	}
	sort.Strings(added)
	return added, nil
}

// requiredFieldSet extracts the set of names from a JSON Schema's "required"
// array. Returns (nil, nil) when the node is nil or has no "required" key —
// that is a legitimate schema with no required fields, not an error.
// Returns an error when "required" exists but has the wrong type or contains a
// non-string element; callers must treat that as a malformed schema and fail closed.
func requiredFieldSet(node *yaml.Node) (map[string]bool, error) {
	if node == nil {
		return nil, nil
	}
	// Decode the node into a generic map to extract "required".
	raw, err := yaml.Marshal(node)
	if err != nil {
		return nil, fmt.Errorf("marshal config schema node: %w", err)
	}
	var schema map[string]any
	if err := yaml.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("unmarshal config schema: %w", err)
	}
	req, ok := schema["required"]
	if !ok {
		return nil, nil
	}
	slice, ok := req.([]any)
	if !ok {
		return nil, fmt.Errorf("config schema \"required\" must be an array, got %T", req)
	}
	set := make(map[string]bool, len(slice))
	for i, v := range slice {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("config schema \"required\"[%d] must be a string, got %T", i, v)
		}
		set[s] = true
	}
	return set, nil
}

// diffServices emits material changes for each service version field.
func diffServices(old, new *sdkmanifest.Manifest) []Change {
	var changes []Change
	if old.Services.Tool != new.Services.Tool {
		changes = append(changes, Change{Field: "services.tool", Material: true, From: old.Services.Tool, To: new.Services.Tool})
	}
	if old.Services.Channel != new.Services.Channel {
		changes = append(changes, Change{Field: "services.channel", Material: true, From: old.Services.Channel, To: new.Services.Channel})
	}
	if old.Services.Trigger != new.Services.Trigger {
		changes = append(changes, Change{Field: "services.trigger", Material: true, From: old.Services.Trigger, To: new.Services.Trigger})
	}
	return changes
}

// diffTier2 emits a single material change when the sorted set of tier-2
// capability declarations changes.
func diffTier2(old, new *sdkmanifest.Manifest) []Change {
	oldSet := sortedJoin(old.Tier2)
	newSet := sortedJoin(new.Tier2)
	if oldSet == newSet {
		return nil
	}
	return []Change{{Field: "tier2", Material: true, From: oldSet, To: newSet}}
}

// sortedJoin sorts a string slice, deduplicates, and joins with commas for
// comparison and display.
func sortedJoin(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	cp := make([]string, len(ss))
	copy(cp, ss)
	sort.Strings(cp)
	// Deduplicate.
	out := cp[:0]
	for i, s := range cp {
		if i == 0 || s != cp[i-1] {
			out = append(out, s)
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		// json.Marshal([]string) is effectively infallible, but if it somehow
		// fails return a deterministic comma-joined fallback over the same slice.
		return strings.Join(out, ",")
	}
	return string(b)
}

// diffAuth compares auth mode, strategy, and OAuth defaults. All are material.
func diffAuth(old, new *sdkmanifest.Manifest) []Change {
	var changes []Change
	if old.Auth.Mode != new.Auth.Mode {
		changes = append(changes, Change{Field: "auth.mode", Material: true, From: old.Auth.Mode, To: new.Auth.Mode})
	}
	if old.Auth.Strategy != new.Auth.Strategy {
		changes = append(changes, Change{Field: "auth.strategy", Material: true, From: old.Auth.Strategy, To: new.Auth.Strategy})
	}
	changes = append(changes, diffOAuthDefaults(old.Auth.OAuthDefaults, new.Auth.OAuthDefaults)...)
	return changes
}

// diffOAuthDefaults compares OAuthDefaultsDecl presence and fields.
func diffOAuthDefaults(old, new *sdkmanifest.OAuthDefaultsDecl) []Change {
	if old == nil && new == nil {
		return nil
	}
	if (old == nil) != (new == nil) {
		from, to := "nil", "set"
		if old != nil {
			from, to = "set", "nil"
		}
		return []Change{{Field: "auth.oauth_defaults", Material: true, From: from, To: to}}
	}
	var changes []Change
	if old.AuthorizationURL != new.AuthorizationURL {
		changes = append(changes, Change{Field: "auth.oauth_defaults.authorization_url", Material: true, From: old.AuthorizationURL, To: new.AuthorizationURL})
	}
	if old.TokenURL != new.TokenURL {
		changes = append(changes, Change{Field: "auth.oauth_defaults.token_url", Material: true, From: old.TokenURL, To: new.TokenURL})
	}
	oldScopes := sortedJoin(old.Scopes)
	newScopes := sortedJoin(new.Scopes)
	if oldScopes != newScopes {
		changes = append(changes, Change{Field: "auth.oauth_defaults.scopes", Material: true, From: oldScopes, To: newScopes})
	}
	if old.HasClientID != new.HasClientID {
		changes = append(changes, Change{Field: "auth.oauth_defaults.has_client_id", Material: true,
			From: fmt.Sprintf("%v", old.HasClientID), To: fmt.Sprintf("%v", new.HasClientID)})
	}
	if old.HasClientSecret != new.HasClientSecret {
		changes = append(changes, Change{Field: "auth.oauth_defaults.has_client_secret", Material: true,
			From: fmt.Sprintf("%v", old.HasClientSecret), To: fmt.Sprintf("%v", new.HasClientSecret)})
	}
	return changes
}

// diffTools compares the declared tool list keyed by Name.
// Added or removed tools are material. For existing names: ApprovalRequired
// flip and InputSchema/OutputSchema canonical-bytes change are material;
// Description change is cosmetic.
func diffTools(old, new *sdkmanifest.Manifest) []Change {
	oldMap := toolDeclMap(old.Tools)
	newMap := toolDeclMap(new.Tools)

	var changes []Change

	for name := range oldMap {
		if _, ok := newMap[name]; !ok {
			changes = append(changes, Change{Field: "tools." + name, Material: true, From: name, To: ""})
		}
	}
	for name := range newMap {
		if _, ok := oldMap[name]; !ok {
			changes = append(changes, Change{Field: "tools." + name, Material: true, From: "", To: name})
		}
	}
	for name, oldTool := range oldMap {
		newTool, ok := newMap[name]
		if !ok {
			continue
		}
		if oldTool.ApprovalRequired != newTool.ApprovalRequired {
			changes = append(changes, Change{
				Field:    "tools." + name + ".approval_required",
				Material: true,
				From:     fmt.Sprintf("%v", oldTool.ApprovalRequired),
				To:       fmt.Sprintf("%v", newTool.ApprovalRequired),
			})
		}
		oldIn := schemautil.ToJSONStripped(oldTool.InputSchema)
		newIn := schemautil.ToJSONStripped(newTool.InputSchema)
		if !bytes.Equal(oldIn, newIn) {
			changes = append(changes, Change{Field: "tools." + name + ".input_schema", Material: true, From: string(oldIn), To: string(newIn)})
		}
		oldOut := schemautil.ToJSONStripped(oldTool.OutputSchema)
		newOut := schemautil.ToJSONStripped(newTool.OutputSchema)
		if !bytes.Equal(oldOut, newOut) {
			changes = append(changes, Change{Field: "tools." + name + ".output_schema", Material: true, From: string(oldOut), To: string(newOut)})
		}
		if oldTool.Description != newTool.Description {
			changes = append(changes, Change{Field: "tools." + name + ".description", Material: false, From: oldTool.Description, To: newTool.Description})
		}
	}
	return changes
}

func toolDeclMap(tools []sdkmanifest.ToolDecl) map[string]sdkmanifest.ToolDecl {
	m := make(map[string]sdkmanifest.ToolDecl, len(tools))
	for _, t := range tools {
		m[t.Name] = t
	}
	return m
}

// diffConfigSchema compares the instance config_schema after stripping cosmetic keys.
func diffConfigSchema(old, new *sdkmanifest.Manifest) []Change {
	oldBytes := schemautil.ToJSONStripped(old.ConfigSchema)
	newBytes := schemautil.ToJSONStripped(new.ConfigSchema)
	if bytes.Equal(oldBytes, newBytes) {
		return nil
	}
	return []Change{{Field: "config_schema", Material: true, From: string(oldBytes), To: string(newBytes)}}
}

// diffSubscriptionSchema compares the manifest-level subscription_schema after
// stripping cosmetic keys. Schema shape changes are material because stored
// scope JSON might no longer validate against the new schema (same reasoning as
// config_schema).
func diffSubscriptionSchema(old, new *sdkmanifest.Manifest) []Change {
	oldBytes := schemautil.ToJSONStripped(old.SubscriptionSchema)
	newBytes := schemautil.ToJSONStripped(new.SubscriptionSchema)
	if bytes.Equal(oldBytes, newBytes) {
		return nil
	}
	return []Change{{Field: "subscription_schema", Material: true, From: string(oldBytes), To: string(newBytes)}}
}

// diffEventKinds compares event_kinds keyed by Kind. Added/removed kinds are
// material. For existing kinds: BindingSchema and PayloadSchema canonical bytes
// are material; Description and Examples are cosmetic.
func diffEventKinds(old, new *sdkmanifest.Manifest) []Change {
	oldMap := eventKindMap(old.EventKinds)
	newMap := eventKindMap(new.EventKinds)

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
		oldPayload := schemautil.ToJSONStripped(oldEK.PayloadSchema)
		newPayload := schemautil.ToJSONStripped(newEK.PayloadSchema)
		if !bytes.Equal(oldPayload, newPayload) {
			changes = append(changes, Change{Field: "event_kinds." + kind + ".payload_schema", Material: true, From: string(oldPayload), To: string(newPayload)})
		}
		if oldEK.Description != newEK.Description {
			changes = append(changes, Change{Field: "event_kinds." + kind + ".description", Material: false, From: oldEK.Description, To: newEK.Description})
		}
	}
	return changes
}

func eventKindMap(kinds []sdkmanifest.EventKindDecl) map[string]sdkmanifest.EventKindDecl {
	m := make(map[string]sdkmanifest.EventKindDecl, len(kinds))
	for _, ek := range kinds {
		m[ek.Kind] = ek
	}
	return m
}
