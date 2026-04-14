import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import type { ApiRun } from '@/api/types'
import { StatusBadge } from '@/components/dashboard/StatusBadge/StatusBadge'
import { TriggerChip } from '@/components/dashboard/TriggerChip/TriggerChip'
import { Button } from '@/components/Button'
import type { RunStatus, TriggerType } from '@/constants/status'
import { formatDurationMs, formatTokens, formatTimestamp, formatProviderName } from '@/utils/format'
import styles from './RunHeader.module.css'

interface CapabilityTool {
  server_name: string
  tool_name: string
  approval: string
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
  showRetry?: boolean
  onRetry?: () => void
}

export function RunHeader({ run, toolCallCount, tokenTotal, duration, capabilitySnapshot, showRetry, onRetry }: Props) {
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
        capabilitySnapshot.provider ? formatProviderName(capabilitySnapshot.provider) : undefined,
        capabilitySnapshot.model,
        `${capabilitySnapshot.toolCount} ${capabilitySnapshot.toolCount === 1 ? 'tool' : 'tools'}`,
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
        {showRetry && onRetry && (
          <Button variant="secondary" size="small" onClick={onRetry}>
            Retry
          </Button>
        )}
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
                    <tr key={`${tool.server_name}/${tool.tool_name}`}>
                      <td className={styles.mono}>{tool.tool_name}</td>
                      <td className={`${styles.mono} ${styles.muted}`}>{tool.server_name}</td>
                      <td>
                        {tool.approval === 'required' ? (
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
