import { Link } from 'react-router-dom'
import type { ApiPolicyListItem } from '@/api/types'
import { usePolicy } from '@/hooks/queries/policies'
import { useRuns } from '@/hooks/queries/runs'
import { yamlToFormState } from '@/components/AgentEditor/agentEditorUtils'
import { formatTokens, formatTimeAgo } from '@/utils/format'
import { StatusBadge } from '@/components/dashboard/StatusBadge'
import { isRunStatus } from '@/constants/status'
import type { RunStatus } from '@/constants/status'
import styles from './PolicyCardExpanded.module.css'

// groupByServer groups tool names like "server.tool_name" by their server prefix.
// Returns an array of "server (N)" strings.
function groupByServer(toolIds: string[]): string[] {
  const counts = new Map<string, number>()
  for (const id of toolIds) {
    const dot = id.indexOf('.')
    const server = dot >= 0 ? id.slice(0, dot) : id
    counts.set(server, (counts.get(server) ?? 0) + 1)
  }
  return Array.from(counts.entries()).map(([server, count]) => `${server} (${count})`)
}

interface Props {
  policy: ApiPolicyListItem
}

export function PolicyCardExpanded({ policy }: Props) {
  const { data: detail, isLoading: detailLoading } = usePolicy(policy.id)
  const { runs, isLoading: runsLoading } = useRuns({
    policy_id: policy.id,
    limit: 3,
    sort: 'started_at',
    order: 'desc',
  })

  if (detailLoading || runsLoading) {
    return (
      <div className={styles.expanded}>
        <span className={styles.loading}>Loading...</span>
      </div>
    )
  }

  const formState = detail ? yamlToFormState(detail.yaml) : null
  const description = formState?.identity.description ?? ''
  const toolIds = formState?.capabilities.tools.map(t => t.toolId) ?? []
  const serverPills = groupByServer(toolIds)
  const maxTokens = formState?.limits.max_tokens_per_run ?? 0
  const maxCalls = formState?.limits.max_tool_calls_per_run ?? 0
  const concurrency = formState?.concurrency.concurrency ?? 'skip'
  const concurrencyLabel = concurrency.charAt(0).toUpperCase() + concurrency.slice(1)

  const hasFeedbackPending = runs.some(r => r.status === 'waiting_for_feedback')

  return (
    <div className={styles.expanded}>
      {description && <p className={styles.description}>{description}</p>}

      <div className={styles.statBar}>
        <div className={styles.stat}>
          <span className={styles.statLabel}>Avg Tokens / Run</span>
          <span className={styles.statValue}>
            {policy.run_count === 0 ? '—' : formatTokens(policy.avg_token_cost)}
          </span>
        </div>
        <div className={styles.divider} />
        <div className={styles.stat}>
          <span className={styles.statLabel}>Limits</span>
          <span className={styles.statValue}>
            {formatTokens(maxTokens)} / {maxCalls}
          </span>
        </div>
        <div className={styles.divider} />
        <div className={styles.stat}>
          <span className={styles.statLabel}>Concurrency</span>
          <span className={styles.statValue}>{concurrencyLabel}</span>
        </div>
      </div>

      {runs.length > 0 && (
        <div>
          <div className={styles.recentLabel}>
            Recent Runs
            {hasFeedbackPending && (
              <span className={styles.feedbackBadge}>feedback pending</span>
            )}
          </div>
          <div className={styles.recentList}>
            {runs.map(run => (
              <Link
                key={run.id}
                to={`/runs/${run.id}`}
                className={styles.recentRow}
              >
                {isRunStatus(run.status) && <StatusBadge status={run.status as RunStatus} />}
                <span className={styles.recentTime}>{formatTimeAgo(run.started_at)}</span>
              </Link>
            ))}
          </div>
        </div>
      )}

      {serverPills.length > 0 && (
        <div>
          <div className={styles.capLabel}>Capabilities</div>
          <div className={styles.capPills}>
            {serverPills.map(pill => (
              <span key={pill} className={styles.capPill}>{pill}</span>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
