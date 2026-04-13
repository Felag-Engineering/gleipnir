import type { ApiPolicyListItem } from '@/api/types'
import { usePolicy } from '@/hooks/queries/policies'
import { useRuns } from '@/hooks/queries/runs'
import { yamlToFormState } from '@/components/PolicyEditor/policyEditorUtils'
import { formatTokens } from '@/utils/format'
import styles from './PolicyCardExpanded.module.css'

const RUN_STATUS_COLORS: Record<string, string> = {
  complete: 'var(--color-green)',
  failed: 'var(--color-red)',
  running: 'var(--color-blue)',
  waiting_for_approval: 'var(--color-amber)',
  waiting_for_feedback: 'var(--color-purple)',
  interrupted: 'var(--color-purple)',
  pending: 'var(--text-muted)',
}

function dotColor(status: string): string {
  return RUN_STATUS_COLORS[status] ?? 'var(--text-muted)'
}

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
    limit: 5,
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

  return (
    <div className={styles.expanded}>
      {description && <p className={styles.description}>{description}</p>}

      <div className={styles.statBar}>
        <div className={styles.stat}>
          <span className={styles.statLabel}>Avg Tokens / Run</span>
          <span className={styles.statValue}>
            {policy.run_count === 0 ? '\u2014' : formatTokens(policy.avg_token_cost)}
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
        {runs.length > 0 && (
          <>
            <div className={styles.divider} />
            <div className={styles.stat}>
              <span className={styles.statLabel}>Recent</span>
              <span className={styles.recentDots}>
                {runs.map(run => (
                  <span
                    key={run.id}
                    className={styles.recentDot}
                    style={{ '--dot-color': dotColor(run.status) } as React.CSSProperties}
                    title={run.status}
                  />
                ))}
              </span>
            </div>
          </>
        )}
      </div>

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
