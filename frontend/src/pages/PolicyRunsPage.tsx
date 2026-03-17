import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { usePolicy } from '@/hooks/usePolicy'
import { queryKeys } from '@/hooks/queryKeys'
import { usePolicyRuns } from '@/hooks/usePolicyRuns'
import SkeletonBlock from '@/components/SkeletonBlock/SkeletonBlock'
import EmptyState from '@/components/EmptyState/EmptyState'
import { StatusBadge } from '@/components/dashboard/StatusBadge'
import { TriggerChip } from '@/components/dashboard/TriggerChip'
import { TriggerRunModal } from '@/components/TriggerRunModal/TriggerRunModal'
import type { RunStatus, TriggerType } from '@/components/dashboard/types'
import { fmtRel, fmtTok, fmtDur } from '@/components/dashboard/styles'
import type { ApiRun } from '@/api/types'
import styles from './PolicyRunsPage.module.css'

const KNOWN_STATUSES = new Set<string>([
  'complete', 'running', 'waiting_for_approval', 'failed', 'interrupted', 'pending',
])
const KNOWN_TRIGGERS = new Set<string>(['webhook', 'cron', 'poll', 'manual'])

function computeDuration(run: ApiRun): number | null {
  if (!run.completed_at) return null
  return Math.floor(
    (new Date(run.completed_at).getTime() - new Date(run.started_at).getTime()) / 1000,
  )
}

export default function PolicyRunsPage() {
  const { id } = useParams<{ id: string }>()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const { data: policy } = usePolicy(id)
  const { data: runsData, status: runsStatus } = usePolicyRuns(id)
  const runs = runsData ?? []
  const [showTriggerModal, setShowTriggerModal] = useState(false)

  const heading = policy?.name ?? id ?? '...'

  function renderContent() {
    if (runsStatus === 'pending') {
      return (
        <div className={styles.skeletonList}>
          <SkeletonBlock height={48} />
          <SkeletonBlock height={48} />
          <SkeletonBlock height={48} />
          <SkeletonBlock height={48} />
          <SkeletonBlock height={48} />
        </div>
      )
    }

    if (runsStatus === 'error') {
      return (
        <div className={styles.errorState}>
          <span>Failed to load runs.</span>
          <button
            className={styles.retryBtn}
            onClick={() => queryClient.invalidateQueries({ queryKey: queryKeys.runs.byPolicy(id!) })}
          >
            Retry
          </button>
        </div>
      )
    }

    if (runs.length === 0) {
      return (
        <EmptyState
          headline="No runs yet"
          subtext="Trigger this policy to see runs here."
          ctaLabel="Edit policy"
          ctaTo={`/policies/${id}`}
        />
      )
    }

    return (
      <div className={styles.table}>
        <div className={styles.headerRow}>
          <span>Run ID</span>
          <span>Status</span>
          <span>Trigger</span>
          <span>Started</span>
          <span>Tokens</span>
          <span>Duration</span>
        </div>
        {runs.map((run) => (
          <Link key={run.id} to={`/runs/${run.id}`} className={styles.row}>
            <span className={styles.runId} title={run.id}>
              {run.id.slice(0, 8)}
            </span>
            <span>
              {KNOWN_STATUSES.has(run.status) && (
                <StatusBadge status={run.status as RunStatus} />
              )}
            </span>
            <span>
              {KNOWN_TRIGGERS.has(run.trigger_type) && (
                <TriggerChip type={run.trigger_type as TriggerType} />
              )}
            </span>
            <span className={styles.muted}>{fmtRel(run.started_at)}</span>
            <span className={styles.mono}>{fmtTok(run.token_cost)}</span>
            <span className={styles.muted}>{fmtDur(computeDuration(run))}</span>
          </Link>
        ))}
      </div>
    )
  }

  return (
    <div className={styles.page}>
      <div className={styles.header}>
        <div className={styles.headerLeft}>
          <Link to="/policies" className={styles.backLink}>
            ← Policies
          </Link>
          <h1 className={styles.title}>{heading}</h1>
        </div>
        <div className={styles.headerActions}>
          <button
            className={styles.playBtn}
            onClick={() => setShowTriggerModal(true)}
            title="Run now"
            aria-label="Run policy now"
          >
            ▶ Run now
          </button>
          <Link to={`/policies/${id}`} className={styles.editLink}>
            Edit policy
          </Link>
        </div>
      </div>
      {renderContent()}
      {showTriggerModal && id && (
        <TriggerRunModal
          policyId={id}
          policyName={heading}
          onClose={() => setShowTriggerModal(false)}
          onSuccess={(runId) => {
            setShowTriggerModal(false)
            navigate(`/runs/${runId}`)
          }}
        />
      )}
    </div>
  )
}
