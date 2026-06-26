package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/felag-engineering/gleipnir/internal/http/httputil"
)

// This file owns the GET /api/v1/admin/plugin-instances endpoint and its
// manifest-derived DTOs. The endpoint is a general plugin-instance projection
// consumed by several surfaces (the audience editor, the trigger picker #219,
// the admin/plugins service badges, and the OAuth re-authorize banner #228/#230)
// — it is not specific to audiences. The method receiver is *AudienceHandler
// because that handler already owns the (store, snap) dependencies this
// projection needs; the route is wired through it in router.go.

// pluginEventExampleDTO is a single named example payload for a plugin event kind.
// Used by the "Test against sample" feature in the policy editor (spec §7.5).
type pluginEventExampleDTO struct {
	Name    string      `json:"name"`
	Payload interface{} `json:"payload"`
}

// pluginToolDTO describes a single tool declared by a plugin's manifest.
// Used by the admin UI detail pane to list what tools the plugin provides.
type pluginToolDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// pluginEventKindDTO is a single event kind from an installed plugin instance's
// manifest — used by the trigger picker to show plugin-sourced trigger options.
type pluginEventKindDTO struct {
	Kind          string                  `json:"kind"`
	Description   string                  `json:"description"`
	Guidance      string                  `json:"guidance,omitempty"`
	BindingSchema interface{}             `json:"binding_schema,omitempty"`
	Examples      []pluginEventExampleDTO `json:"examples,omitempty"`
}

// pluginInstanceDTO describes one installed plugin instance with its
// manifest-derived capabilities. It is the response shape for
// GET /api/v1/admin/plugin-instances and is shared across the audience editor,
// the trigger picker (#219) which reads event_kinds, the admin/plugins page
// service badges, and the OAuth re-authorize banner (#228/#230). Fields added
// here are observed by all of those surfaces — change with care.
type pluginInstanceDTO struct {
	ID                 string               `json:"id"`
	PluginID           string               `json:"plugin_id"`
	PluginName         string               `json:"plugin_name"`
	InstanceName       string               `json:"instance_name"`
	State              string               `json:"state"`
	ImplementsNotify   bool                 `json:"implements_notify"`
	ImplementsRequest  bool                 `json:"implements_request"`
	ConfigSchema       interface{}          `json:"config_schema"`
	EventKinds         []pluginEventKindDTO `json:"event_kinds"`
	SubscriptionSchema interface{}          `json:"subscription_schema"`
	SubscriptionScope  map[string]any       `json:"subscription_scope"`
	Version            int64                `json:"version"`
	// AuthStrategy is the auth strategy declared in the manifest (e.g.
	// "oauth2_authcode", "oauth2_clientcred", "static_api_key"). Used by the
	// admin UI to decide whether to show the Re-authorize button (#228).
	AuthStrategy string `json:"auth_strategy"`
	// HealthDetail is the detail string from the most recent health transition.
	// Omitted when empty. Used together with AuthStrategy to gate the
	// Re-authorize banner: detail prefix "oauth refresh failed" + oauth strategy
	// → show the button.
	HealthDetail string `json:"health_detail,omitempty"`
	// LastOauthCallbackUrl is the OAuth callback URL recorded the last time this
	// instance completed an OAuth flow. Omitted when nil (never authorized). Used
	// by the admin/plugins page to help operators understand which URL needs to be
	// updated in the provider after a public_url change (#230).
	LastOauthCallbackUrl string `json:"last_oauth_callback_url,omitempty"`
	// PluginVersion is the SemVer version string from the manifest (e.g. "1.0.0").
	// Used by the admin/plugins page to display the version on each plugin card.
	PluginVersion string `json:"plugin_version"`
	// Services lists the gRPC services declared by the plugin manifest.
	// Possible values: "tool", "trigger", "channel". Omitted when empty.
	// Used by the admin/plugins page to render service badges on plugin cards.
	Services []string `json:"services,omitempty"`
	// Tools lists tool declarations from the plugin manifest (ToolService plugins
	// only). Each entry carries the tool name and description for the admin UI
	// detail pane. Omitted when the plugin declares no tools.
	Tools []pluginToolDTO `json:"tools,omitempty"`
}

// ListPluginInstances handles GET /api/v1/admin/plugin-instances.
// Returns all installed plugin instances with their manifest-derived
// capabilities for use in the audience editor and trigger picker.
func (h *AudienceHandler) ListPluginInstances(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	instances, err := h.store.Queries().ListPluginInstances(ctx)
	if err != nil {
		slog.Error("list plugin instances: DB error", "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
		return
	}

	dtos := make([]pluginInstanceDTO, 0, len(instances))
	for _, inst := range instances {
		manifest, err := h.snap.ForInstanceID(ctx, inst.ID)
		if err != nil {
			// Skip instances whose manifest cannot be parsed rather than failing
			// the entire list — the operator can still use other instances.
			slog.Warn("list plugin instances: parse manifest", "instance_id", inst.ID, "err", err)
			continue
		}

		var implementsNotify, implementsRequest bool
		var configSchema interface{}
		if len(manifest.Channels) > 0 {
			ch := manifest.Channels[0]
			implementsNotify = ch.ImplementsNotify
			implementsRequest = ch.ImplementsRequest
			if ch.ConfigSchema != nil {
				// Convert the YAML node to a plain map so it serializes as JSON.
				var csMap interface{}
				if err := ch.ConfigSchema.Decode(&csMap); err == nil {
					configSchema = csMap
				}
			}
		}

		eventKinds := make([]pluginEventKindDTO, 0, len(manifest.EventKinds))
		for _, ek := range manifest.EventKinds {
			if ek.Kind == "" {
				continue
			}

			dto := pluginEventKindDTO{
				Kind:        ek.Kind,
				Description: ek.Description,
				Guidance:    ek.Guidance,
			}

			// Decode binding_schema into a plain map so it serializes as JSON.
			if ek.BindingSchema != nil {
				var bsMap interface{}
				if err := ek.BindingSchema.Decode(&bsMap); err == nil {
					dto.BindingSchema = bsMap
				} else {
					slog.Warn("list plugin instances: decode binding_schema", "instance_id", inst.ID, "kind", ek.Kind, "err", err)
				}
			}

			// Decode examples; skip malformed entries rather than failing the request.
			for i, exNode := range ek.Examples {
				var m map[string]any
				if err := exNode.Decode(&m); err != nil {
					slog.Warn("list plugin instances: decode example", "instance_id", inst.ID, "kind", ek.Kind, "index", i, "err", err)
					continue
				}
				name, _ := m["name"].(string)
				payload, payloadOK := m["payload"]
				if name == "" || !payloadOK {
					slog.Warn("list plugin instances: example missing name or payload", "instance_id", inst.ID, "kind", ek.Kind, "index", i)
					continue
				}
				dto.Examples = append(dto.Examples, pluginEventExampleDTO{Name: name, Payload: payload})
			}

			eventKinds = append(eventKinds, dto)
		}

		// Decode manifest-level subscription_schema (OUTSIDE the Channels guard —
		// this is a manifest top-level field, not a per-channel field).
		var subscriptionSchema interface{}
		if manifest.SubscriptionSchema != nil {
			var ssMap interface{}
			if err := manifest.SubscriptionSchema.Decode(&ssMap); err == nil {
				subscriptionSchema = ssMap
			} else {
				slog.Warn("list plugin instances: decode subscription_schema", "instance_id", inst.ID, "err", err)
			}
		}

		// Decode current subscription scope from the raw DB row (no extra query).
		var subscriptionScope map[string]any
		if inst.SubscriptionScopeJson != "" && inst.SubscriptionScopeJson != "{}" {
			if err := json.Unmarshal([]byte(inst.SubscriptionScopeJson), &subscriptionScope); err != nil {
				slog.Warn("list plugin instances: decode subscription_scope_json", "instance_id", inst.ID, "err", err)
				subscriptionScope = map[string]any{}
			}
		} else {
			subscriptionScope = map[string]any{}
		}

		healthDetail := ""
		if inst.HealthDetail != nil {
			healthDetail = *inst.HealthDetail
		}

		lastCallbackURL := ""
		if inst.LastOauthCallbackUrl != nil {
			lastCallbackURL = *inst.LastOauthCallbackUrl
		}

		// Derive the declared service list from the manifest Services field.
		// All instances of the same plugin share the same manifest, so this
		// value is identical across instances.
		var services []string
		if manifest.Services.Tool != "" {
			services = append(services, "tool")
		}
		if manifest.Services.Trigger != "" {
			services = append(services, "trigger")
		}
		if manifest.Services.Channel != "" {
			services = append(services, "channel")
		}

		var tools []pluginToolDTO
		for _, t := range manifest.Tools {
			if t.Name == "" {
				continue
			}
			tools = append(tools, pluginToolDTO{
				Name:        t.Name,
				Description: t.Description,
			})
		}

		dtos = append(dtos, pluginInstanceDTO{
			ID:                   inst.ID,
			PluginID:             inst.PluginID,
			PluginName:           manifest.Name,
			InstanceName:         inst.InstanceName,
			State:                inst.HealthState,
			ImplementsNotify:     implementsNotify,
			ImplementsRequest:    implementsRequest,
			ConfigSchema:         configSchema,
			EventKinds:           eventKinds,
			SubscriptionSchema:   subscriptionSchema,
			SubscriptionScope:    subscriptionScope,
			Version:              inst.Version,
			AuthStrategy:         string(manifest.Auth.Strategy),
			HealthDetail:         healthDetail,
			LastOauthCallbackUrl: lastCallbackURL,
			PluginVersion:        manifest.Version,
			Services:             services,
			Tools:                tools,
		})
	}

	httputil.WriteJSON(w, http.StatusOK, dtos)
}
