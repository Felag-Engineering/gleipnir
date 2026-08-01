import styles from './ProtocolBadge.module.css'

export type ProtocolState = 'modern' | 'legacy' | 'unknown'

// Mirrors internal/mcp/discover.go supportedProtocolVersions. Only the MODERN set
// is mirrored: anything non-empty that is not modern is treated as legacy, so a
// backend addition to knownLegacyProtocolVersions can never turn a badge green.
const MODERN_VERSIONS = new Set(['2026-07-28'])

export function protocolState(version: string | null | undefined): ProtocolState {
  if (!version) return 'unknown'
  return MODERN_VERSIONS.has(version) ? 'modern' : 'legacy'
}

const VARIANT: Record<ProtocolState, string> = {
  modern: styles.modern,
  legacy: styles.legacy,
  unknown: styles.unknown,
}

function labelFor(state: ProtocolState, version: string | null): string {
  switch (state) {
    case 'modern':
      return `Protocol ${version}`
    case 'legacy':
      return 'Legacy protocol'
    case 'unknown':
      return 'Protocol unknown'
  }
}

function titleFor(state: ProtocolState, version: string | null): string {
  switch (state) {
    case 'modern':
      return `Pinned protocol revision (${version}) — the revision Gleipnir negotiates with this source.`
    case 'legacy':
      return `Pinned protocol revision (${version}) — this source is on a legacy revision.`
    case 'unknown':
      return 'Protocol revision not yet determined — run Rediscover to probe this source.'
  }
}

export function ProtocolBadge({ version }: { version: string | null }) {
  const state = protocolState(version)
  return (
    <span className={`${styles.badge} ${VARIANT[state]}`} title={titleFor(state, version)}>
      {labelFor(state, version)}
    </span>
  )
}
