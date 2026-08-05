// Package manifestv2 defines the manifest format for containerized plugins
// (ADR-053/ADR-056, mcp-realignment-spec.md §4 and §7, Amendment 1).
//
// It is a SEPARATE package from plugin-sdk/manifest, not a revision of it. The
// v1 gRPC-subprocess substrate is the live system until the cutover lands, and
// the two formats describe incompatible things — a binary implementing three
// gRPC services versus a signed container running an MCP server. Sharing types
// between them would mean every field carrying a "which substrate?" caveat.
//
// # Shape
//
// A v2 manifest is an MCP registry `server.json` record plus Gleipnir's trust
// and containment fields, and the split is literal: the base fields use the
// registry's own vocabulary (Amendment 1 — do not coin parallel names), and
// everything Gleipnir adds lives under a single `gleipnir:` key. That keeps
// "install from a registry entry + our signature" open as a future distribution
// path without a manifest migration, and makes it obvious at a glance which
// half of a manifest is standard and which half is ours.
//
// # What the manifest is FOR
//
// It is the consent surface: what the plugin MAY do, reviewed by an admin
// before it ever runs. Every field here is something an operator is agreeing
// to — an image, a set of domains it may reach, a resource ceiling, a set of
// profiles that light up host surfaces. That is why parsing is strict and
// validation is fail-closed: a manifest nobody can fully read is a consent
// screen nobody can trust.
package manifestv2

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the only schema_version this package accepts. A manifest
// declaring anything else is rejected rather than best-effort parsed — the
// version exists so an old host refuses a new manifest loudly instead of
// silently ignoring the field that would have contained it.
const SchemaVersion = "2"

// Registry types for Package.RegistryType, using the MCP registry's vocabulary.
const (
	// RegistryTypeOCI is a container image. It is the only registry type a
	// managed plugin can use: the substrate runs containers.
	RegistryTypeOCI = "oci"
)

// Transport types for Transport.Type, using the MCP vocabulary.
const (
	TransportStreamableHTTP = "streamable-http"
)

// Channel assurance levels (spec §4.1). The distinction is how strongly the
// channel authenticates the human acting through it, and the HOST — never the
// plugin — decides what each level may resolve.
const (
	// AssuranceAuthenticated means the channel proves who acted (e.g. a
	// platform-authenticated interaction).
	AssuranceAuthenticated = "authenticated"

	// AssuranceWeak means the actor identity is forgeable (e.g. an email
	// From: header). Default host policy lets these answer information
	// requests but falls a permission request through to the next audience
	// entry.
	AssuranceWeak = "weak"
)

// Elicitation kinds a tool may declare per spec §6.1. Declaring it in the
// manifest is stronger than the runtime convention: it is reviewed at install
// time rather than inferred per request.
const (
	ElicitationKindPermission  = "permission"
	ElicitationKindInformation = "information"
)

// Manifest is a containerized plugin's manifest (manifest.yaml).
type Manifest struct {
	// SchemaVersion must be SchemaVersion ("2").
	SchemaVersion string `yaml:"schema_version"`

	// Name is the plugin's canonical identifier. Registry vocabulary; in the
	// MCP registry this is a reverse-DNS name, and Gleipnir does not narrow
	// that beyond requiring it to be non-empty and free of whitespace.
	Name string `yaml:"name"`

	// Version is the plugin's own version string. Registry vocabulary.
	Version string `yaml:"version"`

	// Description is shown on the install consent screen.
	Description string `yaml:"description,omitempty"`

	// Repository is where the source lives. Registry vocabulary; optional, and
	// purely informational — Gleipnir never fetches it.
	Repository *Repository `yaml:"repository,omitempty"`

	// Package is what actually runs. Registry vocabulary.
	Package Package `yaml:"package"`

	// Gleipnir holds every field that is ours rather than the registry's:
	// trust, containment, and the profiles that light up host surfaces.
	Gleipnir Gleipnir `yaml:"gleipnir"`
}

// Repository identifies the plugin's source repository (registry vocabulary).
type Repository struct {
	URL    string `yaml:"url"`
	Source string `yaml:"source,omitempty"` // e.g. "github"
}

// Package describes the artifact to run (registry vocabulary).
type Package struct {
	// RegistryType is where the artifact comes from. Must be RegistryTypeOCI.
	RegistryType string `yaml:"registry_type"`

	// Identifier is the image reference, which MUST be digest-pinned
	// (repo@sha256:...). A tag is a mutable pointer: an operator consenting to
	// a tag is consenting to whatever that tag points at later, which is not
	// consent. The signed bundle carries the image, so the digest is knowable
	// at authoring time.
	Identifier string `yaml:"identifier"`

	// Version is the human-readable version of the packaged artifact, for
	// display. The digest in Identifier is what actually runs.
	Version string `yaml:"version,omitempty"`

	// Transport is how the host talks to the server inside the container.
	Transport Transport `yaml:"transport"`
}

// Transport describes how to reach the MCP server (registry vocabulary).
type Transport struct {
	// Type must be TransportStreamableHTTP.
	Type string `yaml:"type"`

	// Port is the container port the server listens on. Gleipnir's own field
	// rather than registry vocabulary — the registry describes remotes by URL,
	// while a managed plugin is reached inside its own network by port.
	Port int `yaml:"port,omitempty"`
}

// Gleipnir holds the trust and containment fields layered on the registry base.
type Gleipnir struct {
	// Profiles declares which capability profiles this plugin implements
	// (spec §4). A plugin with no profiles declared is a plain tool provider,
	// which is the baseline every ecosystem MCP server already meets.
	Profiles Profiles `yaml:"profiles,omitempty"`

	// Egress lists the destinations this plugin may reach. The default is
	// deny: a container attaches to an internal-only network and can reach
	// nothing until a grant here says otherwise. An empty list is therefore
	// meaningful and common, not a missing value.
	Egress []EgressGrant `yaml:"egress,omitempty"`

	// Resources caps what one instance may consume. Absent means the host
	// default applies; an admin override wins over both.
	Resources *Resources `yaml:"resources,omitempty"`

	// Tools declares per-tool metadata the host needs at review time. It does
	// NOT enumerate the tool list — that comes from tools/list at runtime, and
	// a manifest claiming a tool the server does not serve would be a lie the
	// host cannot act on. This is for facts about a tool that only matter
	// before it is called, such as its elicitation kind.
	Tools []ToolDecl `yaml:"tools,omitempty"`

	// EventKinds attests which event kinds this plugin may emit. Drift between
	// this and the runtime events/discover response is a health fault, not a
	// silent merge: the manifest is what the admin consented to.
	EventKinds []EventKindDecl `yaml:"event_kinds,omitempty"`

	// ConfigSchema is the JSON Schema for per-instance configuration, held as
	// a raw node so field order and structure survive round-tripping.
	// x-gleipnir-secret and x-gleipnir-options annotations carry over from v1
	// unchanged.
	ConfigSchema *yaml.Node `yaml:"config_schema,omitempty"`

	// UserConfigSchema is the JSON Schema for per-user configuration, for
	// plugins that act on behalf of individual users rather than the instance.
	UserConfigSchema *yaml.Node `yaml:"user_config_schema,omitempty"`

	// SBOM is an optional relative path to a CycloneDX SBOM bundled with the
	// plugin. Surfaced as a badge; never parsed.
	SBOM string `yaml:"sbom,omitempty"`
}

// Profiles declares the capability profiles a plugin implements (spec §4).
//
// It is a struct rather than a list of names because two profiles carry
// configuration (a channel's assurance level, an identity provider's link
// methods), and a list plus a side table for their details would let a plugin
// declare a detail for a profile it never claimed.
type Profiles struct {
	// ToolProvider is the baseline profile. Present with no fields; declared
	// explicitly so a manifest states what it is rather than leaving the host
	// to infer it from absence.
	ToolProvider *ToolProviderProfile `yaml:"tool_provider,omitempty"`

	// EventSource implements io.gleipnir/events.
	EventSource *EventSourceProfile `yaml:"event_source,omitempty"`

	// HumanChannel implements io.gleipnir/channel.
	HumanChannel *HumanChannelProfile `yaml:"human_channel,omitempty"`

	// IdentityProvider participates in actor authorization.
	IdentityProvider *IdentityProviderProfile `yaml:"identity_provider,omitempty"`
}

// Declared returns the names of every declared profile, in a stable order.
// Used for consent-screen rendering and audit records.
func (p Profiles) Declared() []string {
	var out []string
	if p.ToolProvider != nil {
		out = append(out, "tool_provider")
	}
	if p.EventSource != nil {
		out = append(out, "event_source")
	}
	if p.HumanChannel != nil {
		out = append(out, "human_channel")
	}
	if p.IdentityProvider != nil {
		out = append(out, "identity_provider")
	}
	return out
}

// ToolProviderProfile is the baseline profile and carries no configuration.
type ToolProviderProfile struct{}

// EventSourceProfile declares the io.gleipnir/events implementation.
type EventSourceProfile struct{}

// HumanChannelProfile declares the io.gleipnir/channel implementation.
type HumanChannelProfile struct {
	// Assurance is how strongly this channel authenticates the acting human
	// (spec §4.1). Required: a channel that does not say cannot be routed to
	// safely, and defaulting it either way would be the host guessing about
	// somebody else's authentication.
	Assurance string `yaml:"assurance"`
}

// IdentityProviderProfile declares participation in actor authorization.
type IdentityProviderProfile struct {
	// LinkMethods names the ways a Gleipnir user can be linked to an external
	// identity. At least one is required — an identity provider that offers no
	// way to link identities provides no identity.
	LinkMethods []string `yaml:"link_methods"`
}

// EgressGrant is one destination the plugin may reach.
type EgressGrant struct {
	// Domain is a hostname, optionally with a single leading "*." wildcard for
	// subdomains. It is a bare host: no scheme, no path, no port — those are
	// properties of a request, not of a destination an admin consents to.
	Domain string `yaml:"domain"`

	// Reason is shown on the consent screen. Optional but strongly encouraged:
	// "why does this plugin need to reach this host" is the question the
	// reviewing admin is actually asking.
	Reason string `yaml:"reason,omitempty"`
}

// Resources caps one instance's consumption.
type Resources struct {
	// MemoryMB is the hard memory limit in mebibytes.
	MemoryMB int `yaml:"memory_mb,omitempty"`

	// CPUMillicores is the CPU quota in millicores (1000 == one core).
	CPUMillicores int `yaml:"cpu_millicores,omitempty"`
}

// ToolDecl carries per-tool facts the host needs before the tool is called.
type ToolDecl struct {
	// Name is the tool name as the server serves it in tools/list.
	Name string `yaml:"name"`

	// ElicitationKind declares what kind of interruption this tool produces
	// when it asks for input (spec §6.1). Empty means the host falls back to
	// the runtime convention (a requestedSchema with no fields is a permission
	// ask). Declaring it moves the decision to install time, where an admin
	// sees it.
	ElicitationKind string `yaml:"elicitation_kind,omitempty"`
}

// EventKindDecl attests one event kind the plugin may emit.
type EventKindDecl struct {
	// Kind is the event-kind identifier.
	Kind string `yaml:"kind"`

	// Description is shown when an operator binds a policy to this kind.
	Description string `yaml:"description,omitempty"`

	// BindingSchema is the JSON Schema for the typed binding filters a policy
	// may set on this kind (ADR-048). Held as a raw node.
	BindingSchema *yaml.Node `yaml:"binding_schema,omitempty"`
}

// assertKnownKeys rejects any mapping key outside allowed.
//
// It exists because yaml.v3's Decoder.KnownFields(true) does NOT reach inside a
// custom UnmarshalYAML: Node.Decode always runs permissively, so every type
// with a custom unmarshaler has to re-establish strictness itself. Missing that
// would leave exactly the surfaces that need review most — the profile set, the
// Gleipnir extension block — silently accepting anything.
func assertKnownKeys(value *yaml.Node, typeName string, allowed ...string) error {
	if value.Kind != yaml.MappingNode {
		return nil
	}
	known := make(map[string]bool, len(allowed))
	for _, k := range allowed {
		known[k] = true
	}
	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i].Value
		if !known[key] {
			return fmt.Errorf("line %d: field %s not found in type %s",
				value.Content[i].Line, key, typeName)
		}
	}
	return nil
}

// UnmarshalYAML decodes Profiles, rejecting any profile name this host does not
// know. An unrecognized profile is a capability the admin was shown and the
// runtime would never light up.
func (p *Profiles) UnmarshalYAML(value *yaml.Node) error {
	if err := assertKnownKeys(value, "profiles",
		"tool_provider", "event_source", "human_channel", "identity_provider"); err != nil {
		return err
	}
	type plain struct {
		ToolProvider     *ToolProviderProfile     `yaml:"tool_provider,omitempty"`
		EventSource      *EventSourceProfile      `yaml:"event_source,omitempty"`
		HumanChannel     *HumanChannelProfile     `yaml:"human_channel,omitempty"`
		IdentityProvider *IdentityProviderProfile `yaml:"identity_provider,omitempty"`
	}
	var q plain
	if err := value.Decode(&q); err != nil {
		return err
	}
	p.ToolProvider = q.ToolProvider
	p.EventSource = q.EventSource
	p.HumanChannel = q.HumanChannel
	p.IdentityProvider = q.IdentityProvider
	return nil
}

// UnmarshalYAML decodes Gleipnir, populating the *yaml.Node schema fields.
//
// yaml.v3 does not populate *yaml.Node struct fields during normal decoding,
// so the raw-node fields are decoded through a shadow struct. This mirrors the
// same quirk v1 works around in rawnode.go.
func (g *Gleipnir) UnmarshalYAML(value *yaml.Node) error {
	if err := assertKnownKeys(value, "gleipnir",
		"profiles", "egress", "resources", "tools", "event_kinds",
		"config_schema", "user_config_schema", "sbom"); err != nil {
		return err
	}
	type plain struct {
		Profiles         Profiles        `yaml:"profiles,omitempty"`
		Egress           []EgressGrant   `yaml:"egress,omitempty"`
		Resources        *Resources      `yaml:"resources,omitempty"`
		Tools            []ToolDecl      `yaml:"tools,omitempty"`
		EventKinds       []EventKindDecl `yaml:"event_kinds,omitempty"`
		ConfigSchema     yaml.Node       `yaml:"config_schema,omitempty"`
		UserConfigSchema yaml.Node       `yaml:"user_config_schema,omitempty"`
		SBOM             string          `yaml:"sbom,omitempty"`
	}
	var p plain
	if err := value.Decode(&p); err != nil {
		return err
	}
	g.Profiles = p.Profiles
	g.Egress = p.Egress
	g.Resources = p.Resources
	g.Tools = p.Tools
	g.EventKinds = p.EventKinds
	g.SBOM = p.SBOM
	g.ConfigSchema = nodeOrNil(p.ConfigSchema)
	g.UserConfigSchema = nodeOrNil(p.UserConfigSchema)
	return nil
}

// UnmarshalYAML decodes EventKindDecl, populating BindingSchema.
func (e *EventKindDecl) UnmarshalYAML(value *yaml.Node) error {
	if err := assertKnownKeys(value, "event_kind", "kind", "description", "binding_schema"); err != nil {
		return err
	}
	type plain struct {
		Kind          string    `yaml:"kind"`
		Description   string    `yaml:"description,omitempty"`
		BindingSchema yaml.Node `yaml:"binding_schema,omitempty"`
	}
	var p plain
	if err := value.Decode(&p); err != nil {
		return err
	}
	e.Kind = p.Kind
	e.Description = p.Description
	e.BindingSchema = nodeOrNil(p.BindingSchema)
	return nil
}

// nodeOrNil returns a pointer to n, or nil when n was never populated (an
// absent field leaves the zero Node, whose Kind is 0). An explicit YAML null
// yields a non-nil null ScalarNode, which is deliberately distinct from absent.
func nodeOrNil(n yaml.Node) *yaml.Node {
	if n.Kind == 0 {
		return nil
	}
	return &n
}

// digestSeparator splits a digest-pinned image reference into its repository
// and digest halves.
const digestSeparator = "@"

// Repository returns the repository half of a digest-pinned Identifier
// ("ghcr.io/acme/plugin" from "ghcr.io/acme/plugin@sha256:..."), or the whole
// Identifier when it carries no digest. Callers that need a validated
// manifest should Validate first — this accessor parses, it does not judge.
func (p Package) Repository() string {
	if i := strings.Index(p.Identifier, digestSeparator); i >= 0 {
		return p.Identifier[:i]
	}
	return p.Identifier
}

// Digest returns the "sha256:..." half of a digest-pinned Identifier, or "" if
// it carries none. This is the pin the host verifies a loaded image against:
// the whole point of forbidding tags is that this value, not a mutable
// pointer, decides what runs.
func (p Package) Digest() string {
	if i := strings.Index(p.Identifier, digestSeparator); i >= 0 {
		return p.Identifier[i+len(digestSeparator):]
	}
	return ""
}
