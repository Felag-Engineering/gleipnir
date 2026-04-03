import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import type { ApiRun } from '@/api/types'
import { StatusBadge } from '@/components/dashboard/StatusBadge/StatusBadge'
import { TriggerChip } from '@/components/dashboard/TriggerChip/TriggerChip'
import type { RunStatus, TriggerType } from '@/constants/status'
import { formatDurationMs, formatTokens, formatTimestamp } from '@/utils/format'
import styles from './RunHeader.module.css'

interface CapabilityTool {
  ServerName: string
  ToolName: string
  Approval: string
}

interface Props {
  run: ApiRun
  toolCallCount: number
  tokenTotal: number
  duration: number | null
  capabilitySnapshot?: {
    provider?: string
    model?: string
    toolCount: number
    tools: Array<CapabilityTool>
  } | null
}

export function RunHeader({ run, toolCallCount, tokenTotal, duration, capabilitySnapshot }: Props) {
  const navigate = useNavigate()
  const [adminOpen, setAdminOpen] = useState(false)
  const [capExpanded, setCapExpanded] = useState(false)

  const statCards = [
    { value: duration !== null ? formatDurationMs(duration) : '—', label: 'Duration' },
    { value: formatTokens(tokenTotal), label: 'Tokens' },
    { value: String(toolCallCount), label: 'Tool Calls' },
    { value: formatTimestamp(run.started_at), label: 'Started' },
  ]

  const capabilityParts = capabilitySnapshot
    ? [
        capabilitySnapshot.provider,
        capabilitySnapshot.model,
        `${capabilitySnapshot.toolCount} tools`,
      ].filter(Boolean)
    : []

  return (
    <header className={styles.header}>
      <div className={styles.row1}>
        <button
          type="button"
          className={styles.backBtn}
          onClick={() => navigate('/dashboard')}
        >
          ← Runs
        </button>
        <span className={styles.policyName}>
          {run.policy_name || run.policy_id}
        </span>
        <StatusBadge status={run.status as RunStatus} />
        <TriggerChip type={run.trigger_type as TriggerType} />
      </div>

      <div className={styles.statCards}>
        {statCards.map(({ value, label }) => (
          <div key={label} className={styles.statCard}>
            <span className={styles.statValue}>{value}</span>
            <span className={styles.statLabel}>{label}</span>
          </div>
        ))}
      </div>

      {capabilityParts.length > 0 && (
        <div>
          <button
            type="button"
            className={styles.capabilityBar}
            onClick={() => setCapExpanded(o => !o)}
            aria-expanded={capExpanded}
          >
            {capabilityParts.join(' · ')}
            <span className={styles.capabilityChevron}>
              {capExpanded ? ' ▲' : ' ▼'}
            </span>
          </button>
          {capExpanded && capabilitySnapshot && capabilitySnapshot.tools.length > 0 && (
            <div className={styles.capabilityTableWrapper}>
              <table className={styles.capabilityTable}>
                <thead>
                  <tr>
                    <th>Tool</th>
                    <th>Server</th>
                    <th>Approval</th>
                  </tr>
                </thead>
                <tbody>
                  {capabilitySnapshot.tools.map(tool => (
                    <tr key={`${tool.ServerName}/${tool.ToolName}`}>
                      <td className={styles.mono}>{tool.ToolName}</td>
                      <td className={`${styles.mono} ${styles.muted}`}>{tool.ServerName}</td>
                      <td>
                        {tool.Approval === 'required' ? (
                          <span className={styles.approvalRequired}>required</span>
                        ) : (
                          <span className={styles.approvalNone}>none</span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}

      <div className={styles.adminBar}>
        <button
          type="button"
          className={styles.adminToggle}
          onClick={() => setAdminOpen(o => !o)}
          aria-expanded={adminOpen}
        >
          Run details {adminOpen ? '▲' : '▼'}
        </button>
        {adminOpen && (
          <dl className={styles.adminGrid}>
            <div className={styles.adminCell}>
              <dt className={styles.adminLabel}>Run ID</dt>
              <dd className={styles.adminValue}>{run.id}</dd>
            </div>
            <div className={styles.adminCell}>
              <dt className={styles.adminLabel}>Model</dt>
              <dd className={styles.adminValue}>{run.model}</dd>
            </div>
          </dl>
        )}
      </div>
    </header>
  )
}
