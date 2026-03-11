import { EmptyState } from '../components/EmptyState'
import { SkeletonBlock } from '../components/SkeletonBlock'
import { StatsBar } from '../components/dashboard/StatsBar'
import { usePolicies } from '../hooks/usePolicies'
import { useStatsData } from '../hooks/useStatsData'

export default function DashboardPage() {
  const { stats } = useStatsData()
  // usePolicies is also called inside useStatsData — TanStack Query deduplicates the request.
  const { data: policies, status: policiesStatus } = usePolicies()

  function renderPoliciesSection() {
    if (policiesStatus === 'pending') return <SkeletonBlock height={120} />
    if (policiesStatus === 'error') return <p>Failed to load policies.</p>
    if (policies.length === 0) {
      return (
        <EmptyState
          headline="No policies yet"
          subtext="Create your first policy to start running agents"
          ctaLabel="Create policy"
          ctaTo="/policies/new"
        />
      )
    }
    return null
  }

  return (
    <div>
      <h1>Dashboard</h1>
      <StatsBar stats={stats} />
      {renderPoliciesSection()}
    </div>
  )
}
