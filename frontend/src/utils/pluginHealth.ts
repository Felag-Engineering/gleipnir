import type { PluginHealthState } from '@/api/types'

// Severity ranking mirrors internal/plugin/state/pluginstate.go → severity map.
// Higher rank = worse health. The ranking is a design decision from issue #191;
// see that package's godoc for the full rationale.
const SEVERITY: Record<PluginHealthState, number> = {
  healthy:                    0,
  unsigned_permissive:        1,
  pending_key_approval:       2,
  pending_manifest_approval:  2,
  pending_config_migration:   2,
  pending_reauthorize:        2,
  unhealthy:                  3,
  circuit_broken:             4,
  verification_error:         5,
  signature_invalid:          6,
  crashed:                    7,
  // inactive is a deliberate admin disable — severity 8 so worstHealth correctly
  // reflects that a deactivated instance is not operational (issue #243).
  inactive:                   8,
}

// worstHealth returns the most severe state from the given array.
// Returns 'healthy' for an empty array.
export function worstHealth(states: PluginHealthState[]): PluginHealthState {
  let worst: PluginHealthState = 'healthy'
  for (const s of states) {
    if (SEVERITY[s] > SEVERITY[worst]) {
      worst = s
    }
  }
  return worst
}

// RefreshFailureDetailPrefix is the stable prefix written by the backend's
// MarkRefreshFailed into the instance health_detail field. Matching on this
// prefix is the contract for showing the Re-authorize button (#228).
export const RefreshFailureDetailPrefix = 'oauth refresh failed'

// isOAuthRefreshFailure returns true when the instance is unhealthy specifically
// because an OAuth token refresh failed. This gates the Re-authorize banner.
export function isOAuthRefreshFailure(state: string, detail?: string): boolean {
  return state === 'unhealthy' && (detail?.startsWith(RefreshFailureDetailPrefix) ?? false)
}

// pluginHealthLabel returns the human-readable display label for a health state.
export function pluginHealthLabel(state: PluginHealthState): string {
  switch (state) {
    case 'healthy':                   return 'Healthy'
    case 'unsigned_permissive':       return 'Unsigned (permissive)'
    case 'pending_key_approval':      return 'Pending key approval'
    case 'pending_manifest_approval': return 'Pending manifest approval'
    case 'pending_config_migration':  return 'Pending config migration'
    case 'pending_reauthorize':       return 'Pending re-authorize'
    case 'unhealthy':                 return 'Unhealthy'
    case 'circuit_broken':            return 'Circuit broken'
    case 'verification_error':        return 'Verification error'
    case 'signature_invalid':         return 'Signature invalid'
    case 'crashed':                   return 'Crashed'
    case 'inactive':                  return 'Inactive'
  }
}
