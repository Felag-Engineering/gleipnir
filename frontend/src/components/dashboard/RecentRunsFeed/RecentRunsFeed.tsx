import { Link, useNavigate } from 'react-router-dom'
import { ArrowRight } from 'lucide-react'
import { useRuns } from '@/hooks/queries/runs'
import { useSetupReadiness } from '@/hooks/useSetupReadiness'
import { StatusBadge } from '@/components/dashboard/StatusBadge'
import { EmptyState } from '@/components/EmptyState'
import { SkeletonBlock } from '@/components/SkeletonBlock'
import type { RunStatus } from '@/components/dashboard/types'
import {
  formatTimeAgo,
  formatTokens,
  formatDuration,
  computeRunDuration,
} from '@/utils/format'
import styles from './RecentRunsFeed.module.css'

const EMPTY_STATE_BY_STEP = {
  model: {
    headline: 'Start by adding a model API key',
    subtext: 'Configure a provider API key so agents can talk to an LLM.',
    ctaLabel: 'Go to Models',
    ctaTo: '/admin/models',
  },
  server: {
    headline: 'Add an MCP server to give agents tools',
    subtext: 'MCP servers expose the tools your agents can call.',
    ctaLabel: 'Go to Tools',
    ctaTo: '/tools',
  },
  agent: {
    headline: 'Create your first agent',
    subtext: 'Agents bundle a trigger, a model, and the tools they are allowed to use.',
    ctaLabel: 'New Agent',
    ctaTo: '/agents/new',
  },
  ready: {
    headline: 'No runs yet',
    subtext: 'Trigger one of your agents to see runs here.',
    ctaLabel: 'Go to Agents',
    ctaTo: '/agents',
  },
}

export function RecentRunsFeed() {
  const { runs, isLoading } = useRuns({ limit: 10, sort: 'started_at', order: 'desc' })
  const readiness = useSetupReadiness()
  const navigate = useNavigate()

  const emptyStateProps = readiness.isError
    ? EMPTY_STATE_BY_STEP.ready
    : EMPTY_STATE_BY_STEP[readiness.nextStep]

  return (
    <div>
      <div className={styles.header}>
        <span className={styles.title}>RECENT RUNS</span>
        <Link to="/runs" className={styles.viewAll}>
          View all runs
          <ArrowRight size={12} aria-hidden strokeWidth={1.5} />
        </Link>
      </div>

      <div className={styles.container}>
        {isLoading && (
          <>
            {Array.from({ length: 5 }).map((_, i) => (
              <div key={i} className={styles.skeletonRow}>
                <SkeletonBlock height={16} width="40%" />
                <SkeletonBlock height={16} width={80} />
                <SkeletonBlock height={16} width={48} />
                <SkeletonBlock height={16} width={48} />
                <SkeletonBlock height={16} width={64} />
              </div>
            ))}
          </>
        )}

        {!isLoading && !readiness.isLoading && runs.length === 0 && (
          <EmptyState {...emptyStateProps} />
        )}

        {!isLoading && runs.map((run, idx) => {
          const duration = computeRunDuration(run)
          return (
            <div
              key={run.id}
              className={`${styles.row} ${idx < runs.length - 1 ? styles.rowBorder : ''}`}
              onClick={() => navigate(`/runs/${run.id}`)}
              role="button"
              tabIndex={0}
              onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') navigate(`/runs/${run.id}`) }}
            >
              <span className={styles.colPolicy}>
                {run.policy_name || run.policy_id}
              </span>
              <span className={styles.colStatus}>
                <StatusBadge status={run.status as RunStatus} />
              </span>
              <span className={styles.colDuration}>
                {formatDuration(duration)}
              </span>
              <span className={styles.colTokens}>
                {formatTokens(run.token_cost)}
              </span>
              <span className={styles.colTime}>
                {formatTimeAgo(run.created_at)}
              </span>
            </div>
          )
        })}
      </div>
    </div>
  )
}
