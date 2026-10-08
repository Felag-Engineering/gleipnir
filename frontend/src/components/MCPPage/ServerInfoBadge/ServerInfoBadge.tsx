import type { ApiMcpServerInfo } from '@/api/types'
import styles from './ServerInfoBadge.module.css'

// The name and version are server-controlled, untrusted strings: they are only
// ever rendered as React text children (escaped), and clipped visually by CSS.
export function ServerInfoBadge({ info }: { info: ApiMcpServerInfo | null | undefined }) {
  if (!info) return null
  const label = [info.name, info.version].filter(Boolean).join(' ')
  if (!label) return null
  return (
    <span className={styles.badge} title={`Reported by the server: ${label}`}>
      {label}
    </span>
  )
}
