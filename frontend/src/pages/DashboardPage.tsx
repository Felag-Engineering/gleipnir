import { PageHeader } from '@/components/PageHeader'
import { usePageTitle } from '@/hooks/usePageTitle'
import { useRechartsCleanup } from '@/hooks/useRechartsCleanup'
import { RunActivityChart } from '@/components/dashboard/RunActivityChart'
import { CostByModelChart } from '@/components/dashboard/CostByModelChart'
import { AttentionQueue } from '@/components/dashboard/AttentionQueue'
import { RecentRunsFeed } from '@/components/dashboard/RecentRunsFeed'
import { useTimeSeriesStats } from '@/hooks/queries/stats'
import { useAttentionItems } from '@/hooks/useAttentionItems'
import styles from './DashboardPage.module.css'

export default function DashboardPage() {
  usePageTitle('Control Center')
  useRechartsCleanup()
  const timeSeries = useTimeSeriesStats()
  const attention = useAttentionItems()

  return (
    <div className={styles.page}>
      <PageHeader title="Control Center" />
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
