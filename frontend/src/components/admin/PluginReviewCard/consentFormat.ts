import type { ApiPluginV2Consent } from '@/api/types'

type EffectiveResources = ApiPluginV2Consent['effective_resources']

// AUTH_STRATEGY_LABEL gives human-readable names for the strategy strings
// from the manifest (spec §9.1).
export const AUTH_STRATEGY_LABEL: Record<string, string> = {
  none: 'None',
  static_api_key: 'Static API key',
  header_set: 'Custom headers',
  basic_auth: 'Basic auth',
  oauth2_authcode: 'OAuth 2.0 (authorization code)',
  oauth2_clientcred: 'OAuth 2.0 (client credentials)',
}

// Where a resolved limit came from: the plugin's own declaration (which the
// admin consents to here), an admin's per-instance override, or the host.
const LIMIT_SOURCE_LABEL: Record<string, string> = {
  manifest: 'set by the plugin',
  admin_override: 'admin override',
  default: 'host default',
}

export function limitSourceLabel(source: string): string {
  return LIMIT_SOURCE_LABEL[source] ?? source
}

export function formatMemory(memoryMb: number): string {
  return `${memoryMb} MiB`
}

export function formatCpu(cpuMillicores: number): string {
  return `${cpuMillicores} millicores`
}

// Phrased for the operator, not the protocol: it says what the person will be
// asked, since a tool that can pause a run for input is a trust decision.
export function elicitationLabel(kind: string | undefined): string | null {
  switch (kind) {
    case undefined:
    case '':
      return null
    case 'permission':
      return 'May ask for your permission'
    case 'information':
      return 'May ask you for information'
    default:
      return 'May ask for your input'
  }
}

export interface ConsentDiff {
  egressAdded: string[]
  egressRemoved: string[]
  memory: { before: EffectiveResources; after: EffectiveResources } | null
  cpu: { before: EffectiveResources; after: EffectiveResources } | null
}

// diffConsent compares what the admin consented to with the blocked candidate.
// Limits are compared as resolved values: a newly declared figure equal to the
// host default is not a change in what the container is capped at.
export function diffConsent(current: ApiPluginV2Consent, pending: ApiPluginV2Consent): ConsentDiff {
  const before = new Set(current.egress.map((g) => g.domain))
  const after = new Set(pending.egress.map((g) => g.domain))
  const cur = current.effective_resources
  const next = pending.effective_resources
  return {
    egressAdded: [...after].filter((d) => !before.has(d)),
    egressRemoved: [...before].filter((d) => !after.has(d)),
    memory: cur.memory_mb !== next.memory_mb ? { before: cur, after: next } : null,
    cpu: cur.cpu_millicores !== next.cpu_millicores ? { before: cur, after: next } : null,
  }
}
