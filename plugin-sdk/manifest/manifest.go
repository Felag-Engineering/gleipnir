// Package manifest provides types and builder helpers for the Gleipnir plugin
// manifest format.
//
// A plugin manifest declares which services (ToolService, ChannelService,
// TriggerService) a binary implements, the credential strategy, declared tools,
// event kinds, and JSON schemas for per-instance configuration.
package manifest

import "gopkg.in/yaml.v3"

// Tier-2 capability identifiers declared in the manifest under
// tier2_capabilities. Each identifier corresponds to a manifest-declared,
// admin-approved Host RPC (spec §8.2).
const (
	Tier2RunHistoryRead    = "run_history_read"
	Tier2UserDirectoryRead = "user_directory_read"
)

// Manifest is the top-level structure for a Gleipnir plugin manifest file
// (manifest.yaml). It is the install-time authority for UX, gating, and
// consent screens. See docs/developer/plugin-system-spec.md §5.
type Manifest struct {
	// SchemaVersion identifies which manifest schema this file conforms to.
	SchemaVersion string `yaml:"schema_version"`

	// Name is the plugin's canonical identifier (lowercase, hyphens, no spaces).
	Name string `yaml:"name"`

	// Version is the plugin's own SemVer (e.g. "1.0.0"). This is independent
	// of the service versions declared in Services.
	Version string `yaml:"version"`

	// Description is a short human-readable description shown in the admin UI.
	Description string `yaml:"description,omitempty"`

	// Author is the plugin author's name and/or email.
	Author string `yaml:"author,omitempty"`

	// License is the SPDX license identifier (e.g. "MIT").
	License string `yaml:"license,omitempty"`

	// Services declares which gRPC services this binary implements and the
	// service-level versions. Handshake/v1 is always present and not listed.
	Services Services `yaml:"services"`

	// Auth declares the credential strategy for this plugin.
	Auth AuthDecl `yaml:"auth"`

	// Tools lists every tool this binary declares (ToolService plugins only).
	// Must be consistent with what the binary emits via --emit-manifest.
	Tools []ToolDecl `yaml:"tools,omitempty"`

	// EventKinds lists every event kind this binary may emit (TriggerService
	// plugins only).
	EventKinds []EventKindDecl `yaml:"event_kinds,omitempty"`

	// Channels lists every channel this binary implements (ChannelService
	// plugins only).
	Channels []ChannelDecl `yaml:"channels,omitempty"`

	// Tier2 lists any Tier-2 Host RPCs this binary declares (shown in the
	// install consent screen). See spec §8.2.
	Tier2 []string `yaml:"tier2_capabilities,omitempty"`

	// ConfigSchema is a JSON Schema (stored as a raw YAML node to preserve
	// structure without re-ordering) for the per-instance config block.
	// Must be an object schema. nil means no config needed.
	ConfigSchema *yaml.Node `yaml:"config_schema,omitempty"`

	// SBOM is an optional relative path to a CycloneDX SBOM JSON file bundled
	// with the plugin tarball. Gleipnir surfaces it as a badge in the admin UI
	// but does not parse it.
	SBOM string `yaml:"sbom,omitempty"`
}

// UnmarshalYAML implements yaml.Unmarshaler so that *yaml.Node fields are
// correctly decoded. The default yaml.v3 decoder does not populate *yaml.Node
// struct fields; this custom decoder uses value intermediaries and converts.
func (m *Manifest) UnmarshalYAML(value *yaml.Node) error {
	// manifestAlias mirrors Manifest with *yaml.Node fields replaced by
	// yaml.Node (value type) so yaml.v3 populates them correctly.
	type manifestAlias struct {
		SchemaVersion string          `yaml:"schema_version"`
		Name          string          `yaml:"name"`
		Version       string          `yaml:"version"`
		Description   string          `yaml:"description,omitempty"`
		Author        string          `yaml:"author,omitempty"`
		License       string          `yaml:"license,omitempty"`
		Services      Services        `yaml:"services"`
		Auth          AuthDecl        `yaml:"auth"`
		Tools         []ToolDecl      `yaml:"tools,omitempty"`
		EventKinds    []EventKindDecl `yaml:"event_kinds,omitempty"`
		Channels      []ChannelDecl   `yaml:"channels,omitempty"`
		Tier2         []string        `yaml:"tier2_capabilities,omitempty"`
		ConfigSchema  yaml.Node       `yaml:"config_schema,omitempty"`
		SBOM          string          `yaml:"sbom,omitempty"`
	}
	var alias manifestAlias
	if err := value.Decode(&alias); err != nil {
		return err
	}
	m.SchemaVersion = alias.SchemaVersion
	m.Name = alias.Name
	m.Version = alias.Version
	m.Description = alias.Description
	m.Author = alias.Author
	m.License = alias.License
	m.Services = alias.Services
	m.Auth = alias.Auth
	m.Tools = alias.Tools
	m.EventKinds = alias.EventKinds
	m.Channels = alias.Channels
	m.Tier2 = alias.Tier2
	m.SBOM = alias.SBOM
	if alias.ConfigSchema.Kind != 0 {
		node := alias.ConfigSchema
		m.ConfigSchema = &node
	}
	return nil
}

// Services declares which gRPC services the plugin binary implements and the
// versions it targets. Omitting a service means the binary does not implement
// it. The host rejects a manifest with no services declared.
type Services struct {
	// Tool is the ToolService version string (e.g. "v1"). Empty means not
	// implemented.
	Tool string `yaml:"tool,omitempty"`

	// Channel is the ChannelService version string. Empty means not implemented.
	Channel string `yaml:"channel,omitempty"`

	// Trigger is the TriggerService version string. Empty means not implemented.
	Trigger string `yaml:"trigger,omitempty"`
}

// AuthDecl describes the credential strategy for a plugin instance.
// See docs/developer/plugin-system-spec.md §4.4 and §9.
type AuthDecl struct {
	// Mode is the credential mode. "instance_credentials" (v1 default) means
	// one credential set serves all calls. "user_credentials" is v2-only.
	Mode string `yaml:"mode"`

	// Strategy is the auth strategy type: "static_key", "header_set",
	// "oauth_authcode", or "none".
	Strategy string `yaml:"strategy"`

	// OAuthDefaults carries default OAuth parameters baked into the manifest by
	// the plugin author. Only set when Strategy is "oauth_authcode".
	OAuthDefaults *OAuthDefaultsDecl `yaml:"oauth_defaults,omitempty"`
}

// OAuthDefaultsDecl holds default OAuth2 authcode parameters baked into the
// manifest. Instance config may override these for power users with private
// apps.
type OAuthDefaultsDecl struct {
	// AuthorizationURL is the OAuth2 authorization endpoint.
	AuthorizationURL string `yaml:"authorization_url"`

	// TokenURL is the OAuth2 token endpoint.
	TokenURL string `yaml:"token_url"`

	// Scopes is the default set of OAuth2 scopes to request.
	Scopes []string `yaml:"scopes,omitempty"`

	// HasClientID is true when the manifest includes a default client_id.
	// The actual value is not stored here (kept in encrypted credentials).
	HasClientID bool `yaml:"has_client_id,omitempty"`

	// HasClientSecret is true when the manifest includes a default client_secret.
	HasClientSecret bool `yaml:"has_client_secret,omitempty"`
}

// ToolDecl declares a single tool exposed by the plugin's ToolService.
type ToolDecl struct {
	// Name is the tool identifier as registered in the host tool namespace.
	Name string `yaml:"name"`

	// Description is the human-readable description shown to operators and
	// passed to the LLM as the tool's docstring.
	Description string `yaml:"description,omitempty"`

	// InputSchema is a JSON Schema (as a raw YAML node) for the tool's input.
	// Derived from a Go struct type by gen-manifest via jsonschema reflection.
	InputSchema *yaml.Node `yaml:"input_schema,omitempty"`

	// OutputSchema is a JSON Schema (as a raw YAML node) for the tool's output.
	OutputSchema *yaml.Node `yaml:"output_schema,omitempty"`

	// ApprovalRequired indicates that this tool is approval-gated by default.
	// Policy-level overrides still apply (ADR-008).
	ApprovalRequired bool `yaml:"approval_required,omitempty"`
}

// UnmarshalYAML implements yaml.Unmarshaler for ToolDecl.
func (t *ToolDecl) UnmarshalYAML(value *yaml.Node) error {
	type toolAlias struct {
		Name             string    `yaml:"name"`
		Description      string    `yaml:"description,omitempty"`
		InputSchema      yaml.Node `yaml:"input_schema,omitempty"`
		OutputSchema     yaml.Node `yaml:"output_schema,omitempty"`
		ApprovalRequired bool      `yaml:"approval_required,omitempty"`
	}
	var alias toolAlias
	if err := value.Decode(&alias); err != nil {
		return err
	}
	t.Name = alias.Name
	t.Description = alias.Description
	t.ApprovalRequired = alias.ApprovalRequired
	if alias.InputSchema.Kind != 0 {
		node := alias.InputSchema
		t.InputSchema = &node
	}
	if alias.OutputSchema.Kind != 0 {
		node := alias.OutputSchema
		t.OutputSchema = &node
	}
	return nil
}

// EventKindDecl declares a single event kind a TriggerService plugin may emit.
type EventKindDecl struct {
	// Kind is the event kind identifier (e.g. "channel_message").
	Kind string `yaml:"kind"`

	// Description is the human-readable description shown in the policy
	// trigger picker.
	Description string `yaml:"description,omitempty"`

	// BindingSchema is a JSON Schema (as a raw YAML node) for the per-policy
	// binding config block. Rendered as a structured form in the policy editor.
	BindingSchema *yaml.Node `yaml:"binding_schema,omitempty"`

	// PayloadSchema is a JSON Schema (as a raw YAML node) for the event
	// payload emitted by EmitEvent.
	PayloadSchema *yaml.Node `yaml:"payload_schema,omitempty"`

	// Examples provides sample payloads for the "Test binding against sample"
	// feature in the policy editor. See spec §7.5.
	Examples []*yaml.Node `yaml:"examples,omitempty"`
}

// UnmarshalYAML implements yaml.Unmarshaler for EventKindDecl.
func (e *EventKindDecl) UnmarshalYAML(value *yaml.Node) error {
	type ekAlias struct {
		Kind          string      `yaml:"kind"`
		Description   string      `yaml:"description,omitempty"`
		BindingSchema yaml.Node   `yaml:"binding_schema,omitempty"`
		PayloadSchema yaml.Node   `yaml:"payload_schema,omitempty"`
		Examples      []yaml.Node `yaml:"examples,omitempty"`
	}
	var alias ekAlias
	if err := value.Decode(&alias); err != nil {
		return err
	}
	e.Kind = alias.Kind
	e.Description = alias.Description
	if alias.BindingSchema.Kind != 0 {
		node := alias.BindingSchema
		e.BindingSchema = &node
	}
	if alias.PayloadSchema.Kind != 0 {
		node := alias.PayloadSchema
		e.PayloadSchema = &node
	}
	for _, ex := range alias.Examples {
		ex := ex
		e.Examples = append(e.Examples, &ex)
	}
	return nil
}

// ChannelDecl declares the ChannelService capabilities of the plugin.
// A plugin has at most one ChannelDecl (one ChannelService).
type ChannelDecl struct {
	// ImplementsNotify is true when the ChannelService implements Notify.
	ImplementsNotify bool `yaml:"implements_notify,omitempty"`

	// ImplementsRequest is true when the ChannelService implements Request.
	ImplementsRequest bool `yaml:"implements_request,omitempty"`

	// ConfigSchema is a JSON Schema (as a raw YAML node) for the per-audience-
	// entry config block validated when operators configure this channel.
	ConfigSchema *yaml.Node `yaml:"config_schema,omitempty"`
}

// HasTier2 reports whether the manifest declares the given Tier-2 capability.
// The capability string must match one of the Tier2* constants (e.g.
// Tier2RunHistoryRead). Comparison is case-sensitive.
func (m *Manifest) HasTier2(cap string) bool {
	for _, c := range m.Tier2 {
		if c == cap {
			return true
		}
	}
	return false
}

// UnmarshalYAML implements yaml.Unmarshaler for ChannelDecl.
func (c *ChannelDecl) UnmarshalYAML(value *yaml.Node) error {
	type chanAlias struct {
		ImplementsNotify  bool      `yaml:"implements_notify,omitempty"`
		ImplementsRequest bool      `yaml:"implements_request,omitempty"`
		ConfigSchema      yaml.Node `yaml:"config_schema,omitempty"`
	}
	var alias chanAlias
	if err := value.Decode(&alias); err != nil {
		return err
	}
	c.ImplementsNotify = alias.ImplementsNotify
	c.ImplementsRequest = alias.ImplementsRequest
	if alias.ConfigSchema.Kind != 0 {
		node := alias.ConfigSchema
		c.ConfigSchema = &node
	}
	return nil
}
