import { useState } from 'react'
import type { CapabilitySnapshotContent } from './types'
import styles from './CapabilitySnapshotCard.module.css'

interface Props {
  content: CapabilitySnapshotContent
}

export function CapabilitySnapshotCard({ content }: Props) {
  const [expanded, setExpanded] = useState(false)
  const tools = Array.isArray(content) ? content : []
  const count = tools.length

  return (
    <div className={styles.card}>
      <button
        type="button"
        className={styles.summary}
        onClick={() => setExpanded((e) => !e)}
        aria-expanded={expanded}
      >
        <span className={styles.icon}>⚙</span>
        <span className={styles.label}>
          Capability snapshot — {count} tool{count === 1 ? '' : 's'}
        </span>
        <span className={styles.chevron}>{expanded ? '▲' : '▼'}</span>
      </button>
      {expanded && (
        <div className={styles.tableWrapper}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th>Server</th>
                <th>Tool</th>
                <th>Role</th>
                <th>Approval</th>
              </tr>
            </thead>
            <tbody>
              {tools.map((t, i) => (
                <tr key={i}>
                  <td className={styles.mono}>{t.ServerName}</td>
                  <td className={styles.mono}>{t.ToolName}</td>
                  <td>
                    <span className={`${styles.roleBadge} ${styles[t.Role]}`}>
                      {t.Role}
                    </span>
                  </td>
                  <td className={styles.mono}>{t.Approval}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
