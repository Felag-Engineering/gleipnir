import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { EmptyState } from '../components/EmptyState'
import { PolicyList } from '../components/PolicyList'
import { SkeletonBlock } from '../components/SkeletonBlock'
import { TriggerRunModal } from '../components/TriggerRunModal/TriggerRunModal'
import { usePolicies } from '../hooks/usePolicies'
import { queryKeys } from '../hooks/queryKeys'
import styles from './PoliciesPage.module.css'

export default function PoliciesPage() {
  const { data: policies, status: policiesStatus } = usePolicies()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [triggerTarget, setTriggerTarget] = useState<{ id: string; name: string } | null>(null)

  function renderContent() {
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
            onClick={() => queryClient.invalidateQueries({ queryKey: queryKeys.policies.all })}
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
          subtext="Create your first policy to get started"
          ctaLabel="New Policy"
          ctaTo="/policies/new"
        />
      )
    }
    return (
      <PolicyList
        policies={policies}
        linkTo="editor"
        onTrigger={(id: string, name: string) => setTriggerTarget({ id, name })}
      />
    )
  }

  return (
    <div className={styles.page}>
      <div className={styles.header}>
        <h1 className={styles.title}>Policies</h1>
        <Link to="/policies/new" className={styles.newPolicyBtn}>
          New Policy
        </Link>
      </div>
      {renderContent()}
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
