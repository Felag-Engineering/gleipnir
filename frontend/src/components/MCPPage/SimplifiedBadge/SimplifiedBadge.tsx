import { formatProviderName } from '@/utils/format'
import styles from './SimplifiedBadge.module.css'

// joinWithAnd renders a human list: "Google", "Google and OpenAI", or
// "Google, OpenAI and Mistral" — used only in the tooltip, which always
// names every provider regardless of how many there are.
function joinWithAnd(names: string[]): string {
  if (names.length <= 1) return names[0] ?? ''
  const last = names[names.length - 1]
  return `${names.slice(0, -1).join(', ')} and ${last}`
}

// simplifiedLabel renders the chip's visible text. 1-2 providers are named
// directly; 3+ collapse to a count so the chip stays a fixed-width glance
// rather than growing unbounded — the tooltip always names every provider
// in full regardless of count.
export function simplifiedLabel(providers: string[]): string {
  if (providers.length === 0) return ''
  if (providers.length <= 2) {
    return `Simplified for ${providers.map(formatProviderName).join(', ')}`
  }
  return `Simplified for ${providers.length} providers`
}

function simplifiedTitle(providers: string[]): string {
  const names = joinWithAnd(providers.map(formatProviderName))
  return `Gleipnir shows a simplified version of this tool's parameters to ${names}. ` +
    'This runs entirely downstream of enforcement, so it only changes what the model is shown and ' +
    'never widens what the tool is allowed to receive.'
}

// SimplifiedBadge is an informational chip marking a tool whose parameter
// schema is rewritten (never enforcement-weakened) for one or more
// configured providers (spec §10). Renders nothing when providers is empty.
export function SimplifiedBadge({ providers }: { providers: string[] }) {
  if (providers.length === 0) return null

  return (
    <span className={styles.badge} title={simplifiedTitle(providers)}>
      {simplifiedLabel(providers)}
    </span>
  )
}
