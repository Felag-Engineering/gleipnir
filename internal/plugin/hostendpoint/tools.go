package hostendpoint

// ToolNamePrefix marks every host-endpoint tool name. The prefix is what
// makes the host-plane assertion exact: bare method names (`log`,
// `get_credentials`) are plausible names for a legitimate plugin tool, and an
// assertion matching those would fail startup on an innocent plugin. A `/`
// never appears in the `<source>.<tool>` dot-names the shared registry holds
// — it is the extension-method separator (`channel/notify`, `events/listen`),
// not a tool-name character — so a `host/…` name in the registry is
// unambiguously a leak, with no false positives possible.
const ToolNamePrefix = "host/"

// Host-endpoint tool names: the spec §8 method inventory. Kept methods carry
// their hostsvc behaviour across (per-capability SetHealthState per #814;
// EmitMetric's ADR-047 caps); WriteAuditStep and EmitEvent are deliberately
// absent (removed from the inventory — their sequencing is #880); the three
// New methods come from §6.4 and §9.
//
// Declared now, ahead of their handlers (#877–#881), because the host-plane
// assertion has to know the full inventory from the first boot that carries
// this package — a name added later is a name the assertion never guarded.
const (
	ToolGetInstanceConfig   = ToolNamePrefix + "get_instance_config"
	ToolGetCredentials      = ToolNamePrefix + "get_credentials"
	ToolGetRunContext       = ToolNamePrefix + "get_run_context"
	ToolEmitMetric          = ToolNamePrefix + "emit_metric"
	ToolLog                 = ToolNamePrefix + "log"
	ToolSetHealthState      = ToolNamePrefix + "set_health_state"
	ToolRunHistoryRead      = ToolNamePrefix + "run_history_read"
	ToolUserDirectoryRead   = ToolNamePrefix + "user_directory_read"
	ToolAuthorizeActor      = ToolNamePrefix + "authorize_actor"
	ToolSubmitIdentityProof = ToolNamePrefix + "submit_identity_proof"
	ToolGetUserConfig       = ToolNamePrefix + "get_user_config"
)

// ToolNames returns the complete host-endpoint tool inventory. The assertion
// consumes it; later issues attach handlers to exactly these names, and the
// milestone-20 conformance suite pins the two lists against each other.
func ToolNames() []string {
	return []string{
		ToolGetInstanceConfig,
		ToolGetCredentials,
		ToolGetRunContext,
		ToolEmitMetric,
		ToolLog,
		ToolSetHealthState,
		ToolRunHistoryRead,
		ToolUserDirectoryRead,
		ToolAuthorizeActor,
		ToolSubmitIdentityProof,
		ToolGetUserConfig,
	}
}
