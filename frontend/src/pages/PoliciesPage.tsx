import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { EmptyState } from '@/components/EmptyState'
import { PolicyList } from '@/components/PolicyList'
import { TriggerRunModal } from '@/components/TriggerRunModal'
import { PageHeader } from '@/components/PageHeader'
import { usePolicies } from '@/hooks/usePolicies'
import { queryKeys } from '@/hooks/queryKeys'
import { QueryBoundary } from '@/components/QueryBoundary'
import buttonStyles from '@/components/Button/Button.module.css'
import styles from './PoliciesPage.module.css'

export default function PoliciesPage() {
  const { data: policies, status: policiesStatus } = usePolicies()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [triggerTarget, setTriggerTarget] = useState<{ id: string; name: string } | null>(null)

  return (
    <div className={styles.page}>
      <PageHeader title="Policies">
        <Link to="/policies/new" className={`${buttonStyles.button} ${buttonStyles.primary}`}>
          New Policy
        </Link>
      </PageHeader>
      <QueryBoundary
        status={policiesStatus}
        isEmpty={(policies ?? []).length === 0}
        errorMessage="Failed to load policies."
        onRetry={() => queryClient.invalidateQueries({ queryKey: queryKeys.policies.all })}
        emptyState={
          <EmptyState
            headline="No policies yet"
            subtext="Create your first policy to get started"
            ctaLabel="New Policy"
            ctaTo="/policies/new"
          />
        }
      >
        <PolicyList
          policies={policies ?? []}
          linkTo="editor"
          onTrigger={(id: string, name: string) => setTriggerTarget({ id, name })}
        />
      </QueryBoundary>
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
