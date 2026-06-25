import { useState } from 'react'
import { Settings, ChevronDown, ChevronUp, ChevronRight } from 'lucide-react'
import type { CapabilitySnapshotContent, CapabilitySnapshotV2, GrantedToolEntry } from './types'
import { isFeedbackEntry } from './types'
import { formatProviderName } from '@/utils/format'
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
  const realTools = (tools ?? []).filter(t => !isFeedbackEntry(t))
  const feedbackEnabled = (tools ?? []).some(isFeedbackEntry)
  const modelName = isV2 ? (content as CapabilitySnapshotV2).model : undefined
  const provider = isV2 ? (content as CapabilitySnapshotV2).provider : undefined
  const count = realTools.length

  return (
    <div className={styles.card}>
      <button
        type="button"
        className={styles.summary}
        onClick={() => setExpanded((e) => !e)}
        aria-expanded={expanded}
      >
        <Settings size={14} className={styles.icon} aria-hidden />
        <span className={styles.label}>
          Capability snapshot — {count} tool{count === 1 ? '' : 's'}{provider ? ` · ${formatProviderName(provider)}` : ''}{modelName ? ` · ${modelName}` : ''}
        </span>
        {expanded ? <ChevronUp size={14} className={styles.chevron} aria-hidden /> : <ChevronDown size={14} className={styles.chevron} aria-hidden />}
      </button>
      {expanded && (
        <div className={styles.tableWrapper}>
          {feedbackEnabled && (
            <div className={styles.feedbackRow}>
              <span className={styles.feedbackChip}>Feedback</span>
              <span className={styles.feedbackLabel}>gleipnir.ask_operator — human-in-the-loop channel</span>
            </div>
          )}
          <table className={styles.table}>
            <thead>
              <tr>
                <th>Server</th>
                <th>Tool</th>
                <th>Approval</th>
              </tr>
            </thead>
            <tbody>
              {realTools.map((t, i) => (
                <tr key={i}>
                  <td className={styles.mono}>{t.server_name}</td>
                  <td className={styles.mono}>{t.tool_name}</td>
                  <td className={styles.mono}>{t.approval}</td>
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
                {promptExpanded ? <ChevronDown size={12} aria-hidden /> : <ChevronRight size={12} aria-hidden />} System prompt
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
