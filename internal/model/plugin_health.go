package model

// PluginHealthState represents the lifecycle health of a plugin instance.
// States fall into three severity tiers:
//
//	Green  — healthy
//	Yellow — degraded but operational (unsigned_permissive, pending_*, unhealthy)
//	Red    — non-functional (verification_error, signature_invalid, circuit_broken, crashed)
//
// The transition graph and total severity ordering are defined in
// internal/plugin/state. See spec §5.6 and issues #191, #230.
type PluginHealthState string

const (
	PluginHealthStateHealthy                 PluginHealthState = "healthy"
	PluginHealthStateUnsignedPermissive      PluginHealthState = "unsigned_permissive"
	PluginHealthStatePendingKeyApproval      PluginHealthState = "pending_key_approval"
	PluginHealthStatePendingManifestApproval PluginHealthState = "pending_manifest_approval"
	PluginHealthStatePendingConfigMigration  PluginHealthState = "pending_config_migration"
	// PluginHealthStatePendingReauthorize is set by the callback-URL rescan when
	// public_url changes and the instance's recorded OAuth callback URL no longer
	// matches (#230). The admin UI surfaces these instances at the top of /admin/plugins
	// so operators can re-run the OAuth flow.
	PluginHealthStatePendingReauthorize PluginHealthState = "pending_reauthorize"
	PluginHealthStateUnhealthy          PluginHealthState = "unhealthy"
	PluginHealthStateCircuitBroken      PluginHealthState = "circuit_broken"
	PluginHealthStateVerificationError  PluginHealthState = "verification_error"
	PluginHealthStateSignatureInvalid   PluginHealthState = "signature_invalid"
	PluginHealthStateCrashed            PluginHealthState = "crashed"
)
