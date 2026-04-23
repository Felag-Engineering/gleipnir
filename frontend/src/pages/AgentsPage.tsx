import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { EmptyState } from '@/components/EmptyState'
import { PolicyList } from '@/components/AgentList'
import { TriggerRunModal } from '@/components/TriggerRunModal'
import { PageHeader } from '@/components/PageHeader'
import { usePolicies } from '@/hooks/queries/policies'
import { useSetupReadiness } from '@/hooks/useSetupReadiness'
import { queryKeys } from '@/hooks/queryKeys'
import { QueryBoundary } from '@/components/QueryBoundary'
import { usePageTitle } from '@/hooks/usePageTitle'
import buttonStyles from '@/components/Button/Button.module.css'
import styles from './AgentsPage.module.css'

export default function AgentsPage() {
  usePageTitle('Agents')
  const { data: policies, status: policiesStatus } = usePolicies()
  const readiness = useSetupReadiness()
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [triggerTarget, setTriggerTarget] = useState<{ id: string; name: string } | null>(null)

  const emptyStateNode = readiness.nextStep === 'model'
    ? <EmptyState
        headline="Start by adding a model API key"
        subtext="Before you can create an agent, configure a provider API key."
        ctaLabel="Go to Models"
        ctaTo="/admin/models"
      />
    : readiness.nextStep === 'server'
    ? <EmptyState
        headline="Add an MCP server to give agents tools"
        subtext="Agents use MCP tools to do their work. Register a server first."
        ctaLabel="Go to Tools"
        ctaTo="/tools"
      />
    : <EmptyState
        headline="No agents yet"
        subtext="Create your first agent to get started"
        ctaLabel="New Agent"
        ctaTo="/agents/new"
      />

  return (
    <div className={styles.page}>
      <PageHeader title="Agents">
        <Link to="/agents/new" className={`${buttonStyles.button} ${buttonStyles.primary}`}>
          New Agent
        </Link>
      </PageHeader>
      <QueryBoundary
        status={policiesStatus}
        isEmpty={(policies ?? []).length === 0}
        errorMessage="Failed to load agents."
        onRetry={() => queryClient.invalidateQueries({ queryKey: queryKeys.policies.all })}
        emptyState={emptyStateNode}
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
