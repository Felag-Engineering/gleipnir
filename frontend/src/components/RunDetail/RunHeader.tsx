import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { ArrowDown, ArrowLeft, ChevronDown, ChevronUp } from 'lucide-react'
import type { ApiRun } from '@/api/types'
import { StatusBadge } from '@/components/dashboard/StatusBadge/StatusBadge'
import { TriggerChip } from '@/components/dashboard/TriggerChip/TriggerChip'
import { Button } from '@/components/Button'
import type { RunStatus, TriggerType } from '@/constants/status'
import { formatDurationMs, formatTokens, formatTimestamp, formatProviderName, runAgentLabel } from '@/utils/format'
import { CAPABILITY_SNAPSHOT_ANCHOR } from './CapabilitySnapshotCard'
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
    feedbackEnabled?: boolean
  } | null
  showRetry?: boolean
  onRetry?: () => void
  showCancel?: boolean
  onCancel?: () => void
  cancelPending?: boolean
  // True when the run is paused on a tool-initiated permission ask; the
  // status badge then reads "Awaiting Approval" (see runStatusLabel).
  awaitingPermission?: boolean
}

export function RunHeader({ run, toolCallCount, tokenTotal, duration, capabilitySnapshot, showRetry, onRetry, showCancel, onCancel, cancelPending, awaitingPermission = false }: Props) {
  const navigate = useNavigate()
  const [adminOpen, setAdminOpen] = useState(false)

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

  function scrollToSnapshot() {
    document.getElementById(CAPABILITY_SNAPSHOT_ANCHOR)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }

  return (
    <header className={styles.header}>
      <div className={styles.row1}>
        <button
          type="button"
          className={styles.backBtn}
          onClick={() => navigate('/dashboard')}
        >
          <ArrowLeft size={14} aria-hidden /> Runs
        </button>
        <span className={styles.policyName}>
          {runAgentLabel(run)}
        </span>
        <StatusBadge status={run.status as RunStatus} awaitingPermission={awaitingPermission} />
        <TriggerChip type={run.trigger_type as TriggerType} />
        {showRetry && onRetry && !run.policy_deleted && (
          <Button variant="secondary" size="small" onClick={onRetry}>
            Retry
          </Button>
        )}
        {showCancel && onCancel && (
          <Button variant="danger" size="small" disabled={cancelPending} onClick={onCancel}>
            {cancelPending ? 'Cancelling…' : 'Cancel run'}
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
          <div className={styles.capabilityRow}>
            {/* The tool list itself lives in one place: the Capability
                snapshot card at the top of the timeline. This summary keeps
                the model and tool count in view and jumps to that card. */}
            <button
              type="button"
              className={styles.capabilityBar}
              onClick={scrollToSnapshot}
              aria-label={`${capabilityParts.join(' · ')} — show capability snapshot`}
            >
              {capabilityParts.join(' · ')}
              <span className={styles.capabilityChevron}>
                <ArrowDown size={14} aria-hidden />
              </span>
            </button>
            {capabilitySnapshot?.feedbackEnabled && (
              <span className={styles.feedbackChip}>Feedback</span>
            )}
          </div>
        </div>
      )}

      <div className={styles.adminBar}>
        <button
          type="button"
          className={styles.adminToggle}
          onClick={() => setAdminOpen(o => !o)}
          aria-expanded={adminOpen}
        >
          Run details {adminOpen ? <ChevronUp size={14} aria-hidden /> : <ChevronDown size={14} aria-hidden />}
        </button>
        {adminOpen && (
          <dl className={styles.adminGrid}>
            <div className={styles.adminCell}>
              <dt className={styles.adminLabel}>Run ID</dt>
              <dd className={styles.adminValue}>{run.id}</dd>
            </div>
            <div className={styles.adminCell}>
              <dt className={styles.adminLabel}>Policy</dt>
              <dd className={styles.adminValue}>
                {run.policy_deleted ? (
                  runAgentLabel(run)
                ) : (
                  <Link to={`/agents/${run.policy_id}`} className={styles.adminLink}>
                    {runAgentLabel(run)}
                  </Link>
                )}
              </dd>
            </div>
            <div className={styles.adminCell}>
              <dt className={styles.adminLabel}>Model</dt>
              <dd className={styles.adminValue}>{run.model}</dd>
            </div>
            <div className={styles.adminCell}>
              <dt className={styles.adminLabel}>Trigger type</dt>
              <dd className={styles.adminValue}>{run.trigger_type}</dd>
            </div>
            <div className={styles.adminCell}>
              <dt className={styles.adminLabel}>Started at</dt>
              <dd className={styles.adminValue}>{formatTimestamp(run.started_at)}</dd>
            </div>
            <div className={styles.adminCell}>
              <dt className={styles.adminLabel}>Completed at</dt>
              <dd className={styles.adminValue}>
                {run.completed_at ? formatTimestamp(run.completed_at) : '—'}
              </dd>
            </div>
          </dl>
        )}
      </div>
    </header>
  )
}
