import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { EmptyState } from '@/components/EmptyState'
import { PolicyList } from '@/components/PolicyList'
import { TriggerRunModal } from '@/components/TriggerRunModal'
import { PageHeader } from '@/components/PageHeader'
import { usePolicies } from '@/hooks/queries/policies'
import { queryKeys } from '@/hooks/queryKeys'
import { QueryBoundary } from '@/components/QueryBoundary'
import { usePageTitle } from '@/hooks/usePageTitle'
import buttonStyles from '@/components/Button/Button.module.css'
import styles from './PoliciesPage.module.css'

export default function PoliciesPage() {
  usePageTitle('Agents')
  const { data: policies, status: policiesStatus } = usePolicies()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [triggerTarget, setTriggerTarget] = useState<{ id: string; name: string } | null>(null)

  return (
    <div className={styles.page}>
      <PageHeader title="Agents">
        <Link to="/policies/new" className={`${buttonStyles.button} ${buttonStyles.primary}`}>
          New Agent
        </Link>
      </PageHeader>
      <QueryBoundary
        status={policiesStatus}
        isEmpty={(policies ?? []).length === 0}
        errorMessage="Failed to load agents."
        onRetry={() => queryClient.invalidateQueries({ queryKey: queryKeys.policies.all })}
        emptyState={
          <EmptyState
            headline="No agents yet"
            subtext="Create your first agent to get started"
            ctaLabel="New Agent"
            ctaTo="/policies/new"
          />
        }
      >
        <PolicyList
          policies={policies ?? []}
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
