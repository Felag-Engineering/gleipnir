import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from 'react-router-dom'
import { ActivityFeed } from '../components/dashboard/ActivityFeed'
import { StatusBoard } from '../components/dashboard/StatusBoard'
import { OnboardingSteps } from '../components/dashboard/OnboardingSteps'
import { StatsBar } from '../components/dashboard/StatsBar'
import { TriggerRunModal } from '../components/TriggerRunModal/TriggerRunModal'
import { usePolicies } from '../hooks/usePolicies'
import { useStatsData } from '../hooks/useStatsData'
import { useRuns } from '../hooks/useRuns'
import { useMcpServers } from '../hooks/useMcpServers'
import { queryKeys } from '../hooks/queryKeys'
import styles from './DashboardPage.module.css'

export default function DashboardPage() {
  const { activeRuns, pendingApprovals } = useStatsData()
  const { data: policies, status: policiesStatus } = usePolicies()
  const { runs, isLoading: runsLoading } = useRuns({ limit: 20 })
  const { data: servers, isLoading: serversLoading } = useMcpServers()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [triggerTarget, setTriggerTarget] = useState<{ id: string; name: string } | null>(null)

  const mcpServerCount = servers?.length ?? 0

  function renderMainContent() {
    if (policiesStatus === 'pending') {
      return null
    }
    if (policiesStatus === 'error') {
      return (
        <div className={styles.errorState}>
          <span>Failed to load policies.</span>
          <button
            className={styles.retryBtn}
            onClick={() => queryClient.invalidateQueries({ queryKey: queryKeys.policies.all })}
          >
            Retry
          </button>
        </div>
      )
    }
    if (policies.length === 0) {
      return (
        <OnboardingSteps
          hasServers={mcpServerCount > 0}
          hasPolicies={false}
          hasRuns={runs.length > 0}
        />
      )
    }
    return (
      <div className={styles.mainGrid}>
        <ActivityFeed runs={runs} isLoading={runsLoading} />
        <StatusBoard
          policies={policies}
          onTrigger={(id, name) => setTriggerTarget({ id, name })}
        />
      </div>
    )
  }

  return (
    <div className={styles.page}>
      <div className={styles.header}>
        <h1 className={styles.title}>Dashboard</h1>
        <Link to="/policies/new" className={styles.newPolicyBtn}>
          New Policy
        </Link>
      </div>
      <StatsBar
        activeRuns={activeRuns}
        pendingApprovals={pendingApprovals}
        mcpServerCount={mcpServerCount}
        mcpServersLoading={serversLoading}
      />
      {renderMainContent()}
      {triggerTarget && (
        <TriggerRunModal
          policyId={triggerTarget.id}
          policyName={triggerTarget.name}
          onClose={() => setTriggerTarget(null)}
          onSuccess={(runId) => {
            setTriggerTarget(null)
            navigate(`/runs/${runId}`)
          }}
        />
      )}
    </div>
  )
}
