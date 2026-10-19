package admin

import (
	"fmt"

	pluginmanifest "github.com/felag-engineering/gleipnir/internal/plugin/manifest"
	"github.com/felag-engineering/gleipnir/internal/plugin/resources"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
)

// pluginV2Consent is everything a v2 manifest asks an admin to agree to, as the
// consent screen must show it. It is read from the consented manifest_snapshot
// through manifest.Read — the same view the reconciler, the Tier-2 gate and the
// credential handlers act on — so what is shown cannot drift from what is
// enforced. Present on the detail response for v2 rows only; v1 responses
// carry no "v2" key at all.
type pluginV2Consent struct {
	Image     pluginV2Image      `json:"image"`
	Egress    []pluginV2Egress   `json:"egress"`
	Resources *pluginV2Resources `json:"resources"`
	// EffectiveResources is what the container is actually capped at when no
	// instance carries an admin override: the manifest's figure, else the host
	// default, each with its source. The per-instance resolved value (override
	// applied) is on the instance response.
	EffectiveResources pluginV2EffectiveResources `json:"effective_resources"`
	Profiles           pluginV2Profiles           `json:"profiles"`
	Tools              []pluginV2Tool             `json:"tools"`
	EventKinds         []pluginV2Event            `json:"event_kinds"`
	Auth               pluginV2Auth               `json:"auth"`
	ConfigSchema       interface{}                `json:"config_schema"`
}

type pluginV2Image struct {
	// Identifier is the digest-pinned reference exactly as the manifest
	// declares it; Repository and Digest are its two halves, split for display.
	Identifier    string `json:"identifier"`
	Repository    string `json:"repository"`
	Digest        string `json:"digest"`
	RegistryType  string `json:"registry_type"`
	Version       string `json:"version,omitempty"`
	TransportType string `json:"transport_type"`
	TransportPort int    `json:"transport_port,omitempty"`
}

type pluginV2Egress struct {
	Domain string `json:"domain"`
	Reason string `json:"reason,omitempty"`
}

// pluginV2Resources is the manifest's own declaration. The whole object is null
// when the manifest declares none, and a zero field means "host default" —
// neither means unlimited.
type pluginV2Resources struct {
	MemoryMB      int `json:"memory_mb,omitempty"`
	CPUMillicores int `json:"cpu_millicores,omitempty"`
}

// pluginV2EffectiveResources is a resolved limit envelope (resources.Resolve).
// Sources are "manifest", "admin_override" or "default".
type pluginV2EffectiveResources struct {
	MemoryMB      int64  `json:"memory_mb"`
	CPUMillicores int64  `json:"cpu_millicores"`
	MemorySource  string `json:"memory_source"`
	CPUSource     string `json:"cpu_source"`
}

// resolveV2Resources applies the manifest declaration and an optional admin
// override through resources.Resolve, the same function the container create
// path uses, so the figure shown is the figure enforced.
func resolveV2Resources(declared *manifestv2.Resources, override resources.Limits) (pluginV2EffectiveResources, error) {
	var manifestLimits resources.Limits
	if declared != nil {
		var err error
		manifestLimits, err = resources.FromManifestMiB(declared.MemoryMB, declared.CPUMillicores)
		if err != nil {
			return pluginV2EffectiveResources{}, err
		}
	}
	eff := resources.Resolve(manifestLimits, override)
	return pluginV2EffectiveResources{
		MemoryMB:      eff.MemoryBytes >> 20,
		CPUMillicores: eff.CPUMillicores,
		MemorySource:  string(eff.MemorySource),
		CPUSource:     string(eff.CPUSource),
	}, nil
}

type pluginV2Profiles struct {
	Declared              []string `json:"declared"`
	HumanChannelAssurance string   `json:"human_channel_assurance,omitempty"`
	IdentityLinkMethods   []string `json:"identity_link_methods,omitempty"`
}

type pluginV2Tool struct {
	Name            string `json:"name"`
	ElicitationKind string `json:"elicitation_kind,omitempty"`
}

type pluginV2Event struct {
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
}

type pluginV2Auth struct {
	Strategy    string               `json:"strategy"`
	HeaderName  string               `json:"header_name,omitempty"`
	HeaderNames []string             `json:"header_names,omitempty"`
	OAuth       *pluginV2OAuthConfig `json:"oauth_defaults,omitempty"`
}

type pluginV2OAuthConfig struct {
	AuthorizationURL string   `json:"authorization_url,omitempty"`
	TokenURL         string   `json:"token_url"`
	Scopes           []string `json:"scopes"`
}

// buildPluginV2Consent projects a v2 Snapshot onto the consent response.
func buildPluginV2Consent(snap pluginmanifest.Snapshot) (*pluginV2Consent, error) {
	var configSchema interface{}
	if snap.ConfigSchema != nil {
		if err := snap.ConfigSchema.Decode(&configSchema); err != nil {
			return nil, fmt.Errorf("decode config_schema: %w", err)
		}
	}

	out := &pluginV2Consent{
		Image: pluginV2Image{
			Identifier:    snap.Package.Identifier,
			Repository:    snap.Package.Repository(),
			Digest:        snap.Package.Digest(),
			RegistryType:  snap.Package.RegistryType,
			Version:       snap.Package.Version,
			TransportType: snap.Transport.Type,
			TransportPort: snap.Transport.Port,
		},
		Egress:       make([]pluginV2Egress, 0, len(snap.Egress)),
		Profiles:     pluginV2Profiles{Declared: snap.Profiles.Declared()},
		Tools:        make([]pluginV2Tool, 0, len(snap.Tools)),
		EventKinds:   make([]pluginV2Event, 0, len(snap.EventKinds)),
		Auth:         pluginV2Auth{Strategy: snap.Auth.Strategy, HeaderName: snap.Auth.HeaderName, HeaderNames: snap.Auth.HeaderNames},
		ConfigSchema: configSchema,
	}
	if out.Profiles.Declared == nil {
		out.Profiles.Declared = []string{}
	}

	for _, g := range snap.Egress {
		out.Egress = append(out.Egress, pluginV2Egress{Domain: g.Domain, Reason: g.Reason})
	}
	if snap.Resources != nil {
		out.Resources = &pluginV2Resources{
			MemoryMB:      snap.Resources.MemoryMB,
			CPUMillicores: snap.Resources.CPUMillicores,
		}
	}
	effective, err := resolveV2Resources(snap.Resources, resources.Limits{})
	if err != nil {
		return nil, fmt.Errorf("resolve resources: %w", err)
	}
	out.EffectiveResources = effective
	if hc := snap.Profiles.HumanChannel; hc != nil {
		out.Profiles.HumanChannelAssurance = hc.Assurance
	}
	if ip := snap.Profiles.IdentityProvider; ip != nil {
		out.Profiles.IdentityLinkMethods = ip.LinkMethods
	}
	for _, t := range snap.Tools {
		out.Tools = append(out.Tools, pluginV2Tool{Name: t.Name, ElicitationKind: t.ElicitationKind})
	}
	for _, k := range snap.EventKinds {
		out.EventKinds = append(out.EventKinds, pluginV2Event{Kind: k.Kind, Description: k.Description})
	}
	if od := snap.Auth.OAuthDefaults; od != nil {
		scopes := od.Scopes
		if scopes == nil {
			scopes = []string{}
		}
		out.Auth.OAuth = &pluginV2OAuthConfig{
			AuthorizationURL: od.AuthorizationURL,
			TokenURL:         od.TokenURL,
			Scopes:           scopes,
		}
	}
	return out, nil
}
