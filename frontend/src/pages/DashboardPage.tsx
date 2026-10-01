import { PageHeader } from '@/components/PageHeader'
import { usePageTitle } from '@/hooks/usePageTitle'
import { useRechartsCleanup } from '@/hooks/useRechartsCleanup'
import { RunActivityChart } from '@/components/dashboard/RunActivityChart'
import { CostByModelChart } from '@/components/dashboard/CostByModelChart'
import { AttentionQueue } from '@/components/dashboard/AttentionQueue'
import { RecentRunsFeed } from '@/components/dashboard/RecentRunsFeed'
import { SetupChecklist } from '@/components/dashboard/SetupChecklist'
import { useTimeSeriesStats } from '@/hooks/queries/stats'
import { useAttentionItems } from '@/hooks/useAttentionItems'
import { useSetupReadiness } from '@/hooks/useSetupReadiness'
import { useRuns } from '@/hooks/queries/runs'
import { useCurrentUser } from '@/hooks/queries/users'
import styles from './DashboardPage.module.css'

export default function DashboardPage() {
  usePageTitle('Control Center')
  useRechartsCleanup()
  const timeSeries = useTimeSeriesStats()
  const attention = useAttentionItems()
  const readiness = useSetupReadiness()
  const recentRuns = useRuns({ limit: 1 })
  const hasFirstRun = recentRuns.runs.length > 0

  // Only admins and operators can see — or do — the setup steps. For any other
  // role the readiness endpoints answer 403, which would read as "not done"
  // and show an approver or auditor a checklist claiming the instance is
  // unconfigured, with links they cannot use.
  const { data: currentUser } = useCurrentUser()
  const roles = currentUser?.roles ?? []
  const canSetUp = roles.includes('admin') || roles.includes('operator')

  return (
    <div className={styles.page}>
      <PageHeader title="Control Center" />
      {canSetUp && (
        <SetupChecklist
          hasModel={readiness.hasModel}
          hasToolSource={readiness.hasToolSource}
          hasAgent={readiness.hasAgent}
          hasFirstRun={hasFirstRun}
          isLoading={readiness.isLoading || recentRuns.isLoading}
        />
      )}
      <div className={styles.chartGrid}>
        <RunActivityChart
          data={timeSeries.data}
          isLoading={timeSeries.isLoading}
          isError={timeSeries.isError}
          onRetry={() => timeSeries.refetch()}
        />
        <CostByModelChart
          data={timeSeries.data}
          isLoading={timeSeries.isLoading}
          isError={timeSeries.isError}
          onRetry={() => timeSeries.refetch()}
        />
      </div>
      {attention.count > 0 && (
        <AttentionQueue
          items={attention.items}
          count={attention.count}
          onDismiss={attention.dismissFailure}
        />
      )}
      <RecentRunsFeed />
    </div>
  )
}
