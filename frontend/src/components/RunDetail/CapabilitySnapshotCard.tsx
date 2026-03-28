import { useState } from 'react'
import type { CapabilitySnapshotContent, CapabilitySnapshotV2, GrantedToolEntry } from './types'
import styles from './CapabilitySnapshotCard.module.css'

interface Props {
  content: CapabilitySnapshotContent
  systemPrompt?: string | null
}

export function CapabilitySnapshotCard({ content, systemPrompt }: Props) {
  const [expanded, setExpanded] = useState(false)
  const [promptExpanded, setPromptExpanded] = useState(false)

  // Support both the legacy array shape (pre-ADR-023) and the V2 object shape.
  const isV2 = !Array.isArray(content) && content !== null && typeof content === 'object'
  const tools = isV2 ? (content as CapabilitySnapshotV2).tools : (content as GrantedToolEntry[])
  const modelName = isV2 ? (content as CapabilitySnapshotV2).model : undefined
  const provider = isV2 ? (content as CapabilitySnapshotV2).provider : undefined
  const count = tools?.length ?? 0

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
          Capability snapshot — {count} tool{count === 1 ? '' : 's'}{provider ? ` · ${provider}` : ''}{modelName ? ` · ${modelName}` : ''}
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
          {systemPrompt && (
            <>
              <button
                type="button"
                className={styles.promptToggle}
                onClick={() => setPromptExpanded((e) => !e)}
                aria-expanded={promptExpanded}
              >
                {promptExpanded ? '▼' : '▶'} System prompt
              </button>
              {promptExpanded && (
                <pre className={styles.promptBody}>{systemPrompt}</pre>
              )}
            </>
          )}
        </div>
      )}
    </div>
  )
}
