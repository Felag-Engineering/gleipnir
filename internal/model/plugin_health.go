package model

// PluginHealthState represents the lifecycle health of a plugin instance.
// States fall into three severity tiers:
//
//	Green  — healthy
//	Yellow — degraded but operational (unsigned_permissive, pending_*, unhealthy)
//	Red    — non-functional (verification_error, signature_invalid, circuit_broken, crashed)
//
// The transition graph and total severity ordering are defined in
// internal/plugin/state. See spec §5.6 and issue #191.
type PluginHealthState string

const (
	PluginHealthStateHealthy                 PluginHealthState = "healthy"
	PluginHealthStateUnsignedPermissive      PluginHealthState = "unsigned_permissive"
	PluginHealthStatePendingKeyApproval      PluginHealthState = "pending_key_approval"
	PluginHealthStatePendingManifestApproval PluginHealthState = "pending_manifest_approval"
	PluginHealthStatePendingConfigMigration  PluginHealthState = "pending_config_migration"
	PluginHealthStateUnhealthy               PluginHealthState = "unhealthy"
	PluginHealthStateCircuitBroken           PluginHealthState = "circuit_broken"
	PluginHealthStateVerificationError       PluginHealthState = "verification_error"
	PluginHealthStateSignatureInvalid        PluginHealthState = "signature_invalid"
	PluginHealthStateCrashed                 PluginHealthState = "crashed"
)
