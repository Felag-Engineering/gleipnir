// Package manifest provides types and builder helpers for the Gleipnir plugin
// manifest format.
//
// A plugin manifest declares which services (ToolService, ChannelService,
// TriggerService) a binary implements, the credential strategy, declared tools,
// event kinds, and JSON schemas for per-instance configuration.
package manifest

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Tier-2 capability identifiers declared in the manifest under
// tier2_capabilities. Each identifier corresponds to a manifest-declared,
// admin-approved Host RPC (spec §8.2).
const (
	Tier2RunHistoryRead    = "run_history_read"
	Tier2UserDirectoryRead = "user_directory_read"
)

// Auth strategy constants for AuthDecl.Strategy. The canonical values are
// enumerated here so both the host and plugin authors share one source of truth
// (spec §9.1). Use these constants rather than string literals.
const (
	// AuthStrategyNone means the plugin requires no credentials.
	AuthStrategyNone = "none"

	// AuthStrategyStaticAPIKey means the plugin expects a static API key
	// stored in credentials_encrypted.
	AuthStrategyStaticAPIKey = "static_api_key"

	// AuthStrategyHeaderSet means the plugin expects one or more static HTTP
	// headers stored in credentials_encrypted.
	AuthStrategyHeaderSet = "header_set"

	// AuthStrategyBasicAuth means the plugin expects HTTP Basic Auth credentials
	// stored in credentials_encrypted.
	AuthStrategyBasicAuth = "basic_auth"

	// AuthStrategyOAuth2Authcode means the host runs the OAuth2 authorization
	// code flow on behalf of the plugin. The host stores the resulting access +
	// refresh token pair in credentials_encrypted and provides automatic renewal
	// via golang.org/x/oauth2 (spec §9.2).
	AuthStrategyOAuth2Authcode = "oauth2_authcode"

	// AuthStrategyOAuth2Clientcred means the host runs the OAuth2 client
	// credentials flow on behalf of the plugin. No user interaction is required;
	// the host exchanges client_id/client_secret for an access token directly and
	// refreshes it automatically (spec §9.2).
	AuthStrategyOAuth2Clientcred = "oauth2_clientcred"
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

	// SubscriptionSchema is a JSON Schema (stored as a raw YAML node) for the
	// instance-level coarse subscription scope sent in TriggerService.Start as
	// watch_scope_json. Distinct from per-event-kind BindingSchema: scope is
	// configured once on the instance to limit chattiness (spec §4.3, §11.3).
	// nil means no scope config is needed; the plugin receives an empty scope.
	SubscriptionSchema *yaml.Node `yaml:"subscription_schema,omitempty"`

	// SBOM is an optional relative path to a CycloneDX SBOM JSON file bundled
	// with the plugin tarball. Gleipnir surfaces it as a badge in the admin UI
	// but does not parse it.
	SBOM string `yaml:"sbom,omitempty"`
}

// UnmarshalYAML implements yaml.Unmarshaler so that *yaml.Node fields are
// correctly decoded. yaml.v3 does not populate *yaml.Node struct fields; the
// plain struct below uses rawNode for those fields so the decoder can reach
// them. See rawnode.go for the full explanation of the quirk.
func (m *Manifest) UnmarshalYAML(value *yaml.Node) error {
	type plain struct {
		SchemaVersion      string          `yaml:"schema_version"`
		Name               string          `yaml:"name"`
		Version            string          `yaml:"version"`
		Description        string          `yaml:"description,omitempty"`
		Author             string          `yaml:"author,omitempty"`
		License            string          `yaml:"license,omitempty"`
		Services           Services        `yaml:"services"`
		Auth               AuthDecl        `yaml:"auth"`
		Tools              []ToolDecl      `yaml:"tools,omitempty"`
		EventKinds         []EventKindDecl `yaml:"event_kinds,omitempty"`
		Channels           []ChannelDecl   `yaml:"channels,omitempty"`
		Tier2              []string        `yaml:"tier2_capabilities,omitempty"`
		ConfigSchema       rawNode         `yaml:"config_schema,omitempty"`
		SubscriptionSchema rawNode         `yaml:"subscription_schema,omitempty"`
		SBOM               string          `yaml:"sbom,omitempty"`
	}
	var p plain
	if err := value.Decode(&p); err != nil {
		return err
	}
	m.SchemaVersion = p.SchemaVersion
	m.Name = p.Name
	m.Version = p.Version
	m.Description = p.Description
	m.Author = p.Author
	m.License = p.License
	m.Services = p.Services
	m.Auth = p.Auth
	m.Tools = p.Tools
	m.EventKinds = p.EventKinds
	m.Channels = p.Channels
	m.Tier2 = p.Tier2
	m.SBOM = p.SBOM
	// An absent field leaves .Node == nil. An explicit YAML null (~) still
	// yields a non-nil *yaml.Node (a null ScalarNode), identical to the prior
	// Kind != 0 behaviour, so dropping that guard is not a behaviour change.
	m.ConfigSchema = p.ConfigSchema.Node
	m.SubscriptionSchema = p.SubscriptionSchema.Node
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

	// Strategy is the auth strategy type. Valid v1 values (use the
	// AuthStrategy* constants): "none", "static_api_key", "header_set",
	// "basic_auth", "oauth2_authcode", "oauth2_clientcred".
	Strategy string `yaml:"strategy"`

	// OAuthDefaults carries default OAuth2 parameters baked into the manifest
	// by the plugin author. Applies to both "oauth2_authcode" and
	// "oauth2_clientcred" strategies. AuthorizationURL is only meaningful for
	// "oauth2_authcode". Instance config may override these defaults for power
	// users with private apps (client_id/client_secret/scopes/token_url).
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
	type plain struct {
		Name             string  `yaml:"name"`
		Description      string  `yaml:"description,omitempty"`
		InputSchema      rawNode `yaml:"input_schema,omitempty"`
		OutputSchema     rawNode `yaml:"output_schema,omitempty"`
		ApprovalRequired bool    `yaml:"approval_required,omitempty"`
	}
	var p plain
	if err := value.Decode(&p); err != nil {
		return err
	}
	t.Name = p.Name
	t.Description = p.Description
	t.ApprovalRequired = p.ApprovalRequired
	// Absent fields leave .Node == nil; an explicit null (~) yields a non-nil
	// null ScalarNode — identical to the prior Kind != 0 behaviour.
	t.InputSchema = p.InputSchema.Node
	t.OutputSchema = p.OutputSchema.Node
	return nil
}

// EventKindDecl declares a single event kind a TriggerService plugin may emit.
type EventKindDecl struct {
	// Kind is the event kind identifier (e.g. "channel_message").
	Kind string `yaml:"kind"`

	// Description is the human-readable description shown in the policy
	// trigger picker. Keep it short — one line, operator-facing label.
	Description string `yaml:"description,omitempty"`

	// Guidance is a longer "how it fires" help string rendered in the
	// subscribed-trigger dialog. Distinct from Description (which stays the
	// short picker label). Optional — omitempty keeps unaffected manifests
	// byte-stable.
	Guidance string `yaml:"guidance,omitempty"`

	// BindingSchema is a JSON Schema (as a raw YAML node) for the per-policy
	// binding config block. Rendered as a structured form in the policy editor.
	BindingSchema *yaml.Node `yaml:"binding_schema,omitempty"`

	// PayloadSchema is a JSON Schema (as a raw YAML node) for the event
	// payload emitted by EmitEvent.
	PayloadSchema *yaml.Node `yaml:"payload_schema,omitempty"`

	// Examples provides sample payloads for the "Test binding against sample"
	// feature in the policy editor. Each node must conform to the canonical
	// shape {name: string, payload: <typed struct>} so the host decoder can
	// extract the display name and pass the payload to the binding evaluator.
	// See spec §7.5. Use AddEventKindWithExamples for the typed helper.
	Examples []*yaml.Node `yaml:"examples,omitempty"`
}

// Example is a named sample event payload for use with AddEventKindWithExamples.
// Name is shown in the policy editor UI; Payload is a typed Go struct that
// round-trips through yaml.Marshal so the host receives a structured map.
type Example struct {
	Name    string
	Payload any
}

// UnmarshalYAML implements yaml.Unmarshaler for EventKindDecl.
func (e *EventKindDecl) UnmarshalYAML(value *yaml.Node) error {
	type plain struct {
		Kind          string    `yaml:"kind"`
		Description   string    `yaml:"description,omitempty"`
		Guidance      string    `yaml:"guidance,omitempty"`
		BindingSchema rawNode   `yaml:"binding_schema,omitempty"`
		PayloadSchema rawNode   `yaml:"payload_schema,omitempty"`
		Examples      []rawNode `yaml:"examples,omitempty"`
	}
	var p plain
	if err := value.Decode(&p); err != nil {
		return err
	}
	e.Kind = p.Kind
	e.Description = p.Description
	e.Guidance = p.Guidance
	// Absent fields leave .Node == nil; an explicit null (~) yields a non-nil
	// null ScalarNode — identical to the prior Kind != 0 behaviour.
	e.BindingSchema = p.BindingSchema.Node
	e.PayloadSchema = p.PayloadSchema.Node
	for _, ex := range p.Examples {
		e.Examples = append(e.Examples, ex.Node)
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

// AddEventKind appends an EventKindDecl to m.EventKinds. When filterStruct is
// nil, the declaration has no binding schema (valid for event kinds that carry
// no operator-configurable binding). When filterStruct is non-nil, ReflectSchema
// is called to derive the BindingSchema; any reflection error is returned
// wrapped with the event kind name for context.
//
// payloadSchema and examples are optional (pass nil/zero values to omit).
func (m *Manifest) AddEventKind(kind, description string, filterStruct any, payloadSchema *yaml.Node, examples ...*yaml.Node) error {
	decl := EventKindDecl{
		Kind:          kind,
		Description:   description,
		PayloadSchema: payloadSchema,
		Examples:      examples,
	}
	if filterStruct != nil {
		node, err := ReflectSchema(filterStruct)
		if err != nil {
			return fmt.Errorf("manifest: reflect binding_schema for event kind %q: %w", kind, err)
		}
		decl.BindingSchema = node
	}
	m.EventKinds = append(m.EventKinds, decl)
	return nil
}

// MustAddEventKind is the panicking variant of AddEventKind. A nil filterStruct
// is the documented no-binding form and does NOT cause a panic; only invopop
// reflection failures panic.
func (m *Manifest) MustAddEventKind(kind, description string, filterStruct any, payloadSchema *yaml.Node, examples ...*yaml.Node) {
	if err := m.AddEventKind(kind, description, filterStruct, payloadSchema, examples...); err != nil {
		panic(err)
	}
}

// AddEventKindWithExamples is the typed variant of AddEventKind. It accepts a
// slice of Example values, marshals each one into a *yaml.Node of shape
// {name: string, payload: <struct>}, then delegates to AddEventKind. This is
// the canonical path for plugin authors who want compile-time safety on their
// example payloads.
//
// The first marshal error short-circuits with the example name for context.
// Zero examples is valid: the declaration is added with no sample payloads.
func (m *Manifest) AddEventKindWithExamples(kind, description string, filterStruct any, payloadSchema *yaml.Node, examples ...Example) error {
	if len(examples) == 0 {
		return m.AddEventKind(kind, description, filterStruct, payloadSchema)
	}
	nodes := make([]*yaml.Node, 0, len(examples))
	for _, ex := range examples {
		raw, err := yaml.Marshal(map[string]any{
			"name":    ex.Name,
			"payload": ex.Payload,
		})
		if err != nil {
			return fmt.Errorf("example %q: %w", ex.Name, err)
		}
		var node yaml.Node
		if err := yaml.Unmarshal(raw, &node); err != nil {
			return fmt.Errorf("example %q: unmarshal node: %w", ex.Name, err)
		}
		// yaml.Unmarshal wraps the root in a DocumentNode; unwrap to the
		// inner MappingNode so callers get consistent node shapes.
		if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
			inner := node.Content[0]
			nodes = append(nodes, inner)
		} else {
			nodes = append(nodes, &node)
		}
	}
	return m.AddEventKind(kind, description, filterStruct, payloadSchema, nodes...)
}

// MustAddEventKindWithExamples is the panicking variant of
// AddEventKindWithExamples. It panics on any marshal or reflection error so
// plugin authors can call it at package init time without error propagation.
func (m *Manifest) MustAddEventKindWithExamples(kind, description string, filterStruct any, payloadSchema *yaml.Node, examples ...Example) {
	if err := m.AddEventKindWithExamples(kind, description, filterStruct, payloadSchema, examples...); err != nil {
		panic(err)
	}
}

// AddEventKindWithGuidance is the guidance-aware variant of
// AddEventKindWithExamples. It calls AddEventKindWithExamples and then sets
// the Guidance field on the appended EventKindDecl. Use this when you want to
// provide a longer "how it fires" help string in addition to the short
// Description (the trigger-picker label). The guidance is rendered in the
// subscribed-trigger dialog and is intentionally omitted from the material
// diff (cosmetic change; must not block hot-reload).
//
// S1: err from AddEventKindWithExamples is checked before indexing so a failed
// append cannot corrupt the last element or panic.
func (m *Manifest) AddEventKindWithGuidance(kind, description, guidance string, filterStruct any, payloadSchema *yaml.Node, examples ...Example) error {
	if err := m.AddEventKindWithExamples(kind, description, filterStruct, payloadSchema, examples...); err != nil {
		return err
	}
	m.EventKinds[len(m.EventKinds)-1].Guidance = guidance
	return nil
}

// MustAddEventKindWithGuidance is the panicking variant of
// AddEventKindWithGuidance. It panics on any marshal or reflection error so
// plugin authors can call it at package init time without error propagation.
func (m *Manifest) MustAddEventKindWithGuidance(kind, description, guidance string, filterStruct any, payloadSchema *yaml.Node, examples ...Example) {
	if err := m.AddEventKindWithGuidance(kind, description, guidance, filterStruct, payloadSchema, examples...); err != nil {
		panic(err)
	}
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
	type plain struct {
		ImplementsNotify  bool    `yaml:"implements_notify,omitempty"`
		ImplementsRequest bool    `yaml:"implements_request,omitempty"`
		ConfigSchema      rawNode `yaml:"config_schema,omitempty"`
	}
	var p plain
	if err := value.Decode(&p); err != nil {
		return err
	}
	c.ImplementsNotify = p.ImplementsNotify
	c.ImplementsRequest = p.ImplementsRequest
	// Absent field leaves .Node == nil; an explicit null (~) yields a non-nil
	// null ScalarNode — identical to the prior Kind != 0 behaviour.
	c.ConfigSchema = p.ConfigSchema.Node
	return nil
}
