import { useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { ApprovalBanner } from '../components/ApprovalBanner'
import { EmptyState } from '../components/EmptyState'
import { PolicyList } from '../components/PolicyList'
import { SkeletonBlock } from '../components/SkeletonBlock'
import { StatsBar } from '../components/dashboard/StatsBar'
import { PolicyList } from '../components/dashboard/PolicyList'
import { usePolicies } from '../hooks/usePolicies'
import { useStatsData } from '../hooks/useStatsData'
import styles from './DashboardPage.module.css'

export default function DashboardPage() {
  const { stats, isLoading, isError } = useStatsData()
  // usePolicies is also called inside useStatsData — TanStack Query deduplicates the request.
  const { data: policies, status: policiesStatus } = usePolicies()
  const queryClient = useQueryClient()

  const approvalCount = (policies ?? []).filter(
    p => p.latest_run?.status === 'waiting_for_approval',
  ).length

  function renderStatsSection() {
    if (isLoading) {
      return (
        <div className={styles.skeletonGrid}>
          <SkeletonBlock height={80} borderRadius={8} />
          <SkeletonBlock height={80} borderRadius={8} />
          <SkeletonBlock height={80} borderRadius={8} />
          <SkeletonBlock height={80} borderRadius={8} />
        </div>
      )
    }
    if (isError) return null
    return <StatsBar stats={stats} />
  }

  function renderPoliciesSection() {
    if (policiesStatus === 'pending') {
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
    if (policiesStatus === 'error') {
      return (
        <div className={styles.errorState}>
          <span>Failed to load policies.</span>
          <button
            className={styles.retryBtn}
            onClick={() => queryClient.invalidateQueries({ queryKey: ['policies'] })}
          >
            Retry
          </button>
        </div>
      )
    }
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
    return <PolicyList policies={policies} />
  }

  return (
    <div className={styles.page}>
      <div className={styles.header}>
        <h1 className={styles.title}>Dashboard</h1>
        <Link to="/policies/new" className={styles.newPolicyBtn}>
          New Policy
        </Link>
      </div>
      <ApprovalBanner count={approvalCount} />
      {renderStatsSection()}
      {renderPoliciesSection()}
    </div>
  )
}
