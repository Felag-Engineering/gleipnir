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
import styles from './DashboardPage.module.css'

export default function DashboardPage() {
  usePageTitle('Control Center')
  useRechartsCleanup()
  const timeSeries = useTimeSeriesStats()
  const attention = useAttentionItems()
  const readiness = useSetupReadiness()
  const recentRuns = useRuns({ limit: 1 })
  const hasFirstRun = recentRuns.runs.length > 0

  return (
    <div className={styles.page}>
      <PageHeader title="Control Center" />
      <SetupChecklist
        hasModel={readiness.hasModel}
        hasServer={readiness.hasServer}
        hasAgent={readiness.hasAgent}
        hasFirstRun={hasFirstRun}
        isLoading={readiness.isLoading || recentRuns.isLoading}
      />
      <div className={styles.chartGrid}>
        <RunActivityChart data={timeSeries.data} isLoading={timeSeries.isLoading} />
        <CostByModelChart data={timeSeries.data} isLoading={timeSeries.isLoading} />
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
