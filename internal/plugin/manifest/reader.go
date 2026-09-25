// reader.go gives host code ONE type to read a plugin manifest snapshot
// through, whichever schema version wrote it. Three host subsystems —
// OAuth/credentials, Tier-2 gating, and event/config/subscription consumers —
// currently read v1's plugin-sdk/manifest fields directly. plugin-sdk/manifestv2
// gained the equivalent auth and tier2_capabilities fields in the same change
// that added this reader, but no host code reads them through it yet — routing
// those three call sites through Read is issue #950. Until v1 is deleted, both
// formats are live at once, and a call site that switches on version itself is
// a call site that will forget to when the next field is added. Read is that
// single seam.
package manifest

import (
	"fmt"

	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
	"gopkg.in/yaml.v3"
)

// ToolDecl is Snapshot's version-neutral per-tool declaration. Description,
// InputSchema, OutputSchema and ApprovalRequired are v1-only (v2 does not
// enumerate the tool list in the manifest — that comes from tools/list at
// runtime); ElicitationKind is v2-only. A field the source version does not
// carry is left at its zero value.
type ToolDecl struct {
	Name             string
	Description      string
	InputSchema      *yaml.Node
	OutputSchema     *yaml.Node
	ApprovalRequired bool
	ElicitationKind  string
}

// EventKindDecl is Snapshot's version-neutral per-event-kind declaration.
// PayloadSchema and Examples are v1-only; Operators (the ADR-052 attested
// allowed-operator set) is v2-only.
type EventKindDecl struct {
	Kind          string
	Description   string
	Guidance      string
	BindingSchema *yaml.Node
	PayloadSchema *yaml.Node
	Examples      []*yaml.Node
	Operators     map[string][]string
}

// AuthDecl is Snapshot's version-neutral credential-strategy declaration.
// Mode is v1-only; HeaderName and HeaderNames are v2-only.
type AuthDecl struct {
	Mode          string
	Strategy      string
	HeaderName    string
	HeaderNames   []string
	OAuthDefaults *OAuthDefaultsDecl
}

// OAuthDefaultsDecl is Snapshot's version-neutral OAuth2 defaults
// declaration. HasClientID and HasClientSecret are v1-only.
type OAuthDefaultsDecl struct {
	AuthorizationURL string
	TokenURL         string
	Scopes           []string
	HasClientID      bool
	HasClientSecret  bool
}

// Snapshot is what a manifest snapshot says, independent of which schema
// version produced it. Profiles, Transport and Egress are v2-only — a v1
// snapshot leaves them at their zero value, not an error, because v1 has no
// concept of any of the three.
type Snapshot struct {
	// Version is the schema version the manifest declared: 1 or 2.
	Version int

	Tools              []ToolDecl
	EventKinds         []EventKindDecl
	ConfigSchema       *yaml.Node
	SubscriptionSchema *yaml.Node
	Auth               AuthDecl
	Tier2Capabilities  []string

	// Profiles, Transport and Egress are v2 only.
	Profiles  manifestv2.Profiles
	Transport manifestv2.Transport
	Egress    []manifestv2.EgressGrant
}

// Read parses snapshot and returns a version-neutral Snapshot, dispatching on
// manifestv2.IsV2's schema_version sniff.
//
// It is fail-closed (the same rule ADR-049 applies to an unparseable manifest
// elsewhere): a manifest that does not parse — or, for v2, does not
// validate — returns an error rather than a partial or best-effort Snapshot.
// v1 has no analogous Validate step (see plugin-sdk/manifest), so a v1
// snapshot fails closed on a decode error only.
func Read(snapshot []byte) (Snapshot, error) {
	if manifestv2.IsV2(snapshot) {
		return readV2(snapshot)
	}
	return readV1(snapshot)
}

func readV1(data []byte) (Snapshot, error) {
	var m sdkmanifest.Manifest
	if err := sdkmanifest.Unmarshal(data, &m); err != nil {
		return Snapshot{}, fmt.Errorf("manifest reader: parse v1 manifest: %w", err)
	}

	tools := make([]ToolDecl, len(m.Tools))
	for i, t := range m.Tools {
		tools[i] = ToolDecl{
			Name:             t.Name,
			Description:      t.Description,
			InputSchema:      t.InputSchema,
			OutputSchema:     t.OutputSchema,
			ApprovalRequired: t.ApprovalRequired,
		}
	}

	kinds := make([]EventKindDecl, len(m.EventKinds))
	for i, k := range m.EventKinds {
		kinds[i] = EventKindDecl{
			Kind:          k.Kind,
			Description:   k.Description,
			Guidance:      k.Guidance,
			BindingSchema: k.BindingSchema,
			PayloadSchema: k.PayloadSchema,
			Examples:      k.Examples,
		}
	}

	return Snapshot{
		Version:            1,
		Tools:              tools,
		EventKinds:         kinds,
		ConfigSchema:       m.ConfigSchema,
		SubscriptionSchema: m.SubscriptionSchema,
		Auth:               authFromV1(m.Auth),
		Tier2Capabilities:  m.Tier2,
	}, nil
}

func authFromV1(a sdkmanifest.AuthDecl) AuthDecl {
	out := AuthDecl{Mode: a.Mode, Strategy: a.Strategy}
	if a.OAuthDefaults != nil {
		out.OAuthDefaults = &OAuthDefaultsDecl{
			AuthorizationURL: a.OAuthDefaults.AuthorizationURL,
			TokenURL:         a.OAuthDefaults.TokenURL,
			Scopes:           a.OAuthDefaults.Scopes,
			HasClientID:      a.OAuthDefaults.HasClientID,
			HasClientSecret:  a.OAuthDefaults.HasClientSecret,
		}
	}
	return out
}

func readV2(data []byte) (Snapshot, error) {
	m, err := manifestv2.Parse(data)
	if err != nil {
		return Snapshot{}, fmt.Errorf("manifest reader: parse v2 manifest: %w", err)
	}

	tools := make([]ToolDecl, len(m.Gleipnir.Tools))
	for i, t := range m.Gleipnir.Tools {
		tools[i] = ToolDecl{
			Name:            t.Name,
			ElicitationKind: t.ElicitationKind,
		}
	}

	kinds := make([]EventKindDecl, len(m.Gleipnir.EventKinds))
	for i, k := range m.Gleipnir.EventKinds {
		kinds[i] = EventKindDecl{
			Kind:          k.Kind,
			Description:   k.Description,
			Guidance:      k.Guidance,
			BindingSchema: k.BindingSchema,
			Operators:     k.Operators,
		}
	}

	var subscriptionSchema *yaml.Node
	if m.Gleipnir.Profiles.EventSource != nil {
		subscriptionSchema = m.Gleipnir.Profiles.EventSource.SubscriptionSchema
	}

	return Snapshot{
		Version:            2,
		Tools:              tools,
		EventKinds:         kinds,
		ConfigSchema:       m.Gleipnir.ConfigSchema,
		SubscriptionSchema: subscriptionSchema,
		Auth:               authFromV2(m.Gleipnir.Auth),
		Tier2Capabilities:  m.Gleipnir.Tier2Capabilities,
		Profiles:           m.Gleipnir.Profiles,
		Transport:          m.Package.Transport,
		Egress:             m.Gleipnir.Egress,
	}, nil
}

func authFromV2(a *manifestv2.AuthDecl) AuthDecl {
	if a == nil {
		return AuthDecl{}
	}
	out := AuthDecl{
		Strategy:    a.Strategy,
		HeaderName:  a.HeaderName,
		HeaderNames: a.HeaderNames,
	}
	if a.OAuthDefaults != nil {
		out.OAuthDefaults = &OAuthDefaultsDecl{
			AuthorizationURL: a.OAuthDefaults.AuthorizationURL,
			TokenURL:         a.OAuthDefaults.TokenURL,
			Scopes:           a.OAuthDefaults.Scopes,
		}
	}
	return out
}
