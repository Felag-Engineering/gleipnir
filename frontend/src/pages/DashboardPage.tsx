import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../api/fetch'
import { EmptyState } from '../components/EmptyState'
import { SkeletonBlock } from '../components/SkeletonBlock'

function RunsQueryDemo() {
  const { status, error } = useQuery({
    queryKey: ['runs'],
    queryFn: () => apiFetch<unknown[]>('/runs'),
  })

  if (status === 'pending') return <p>Loading runs…</p>
  if (status === 'error') return <p>Runs query error (expected): {String(error)}</p>
  return <p>Runs loaded.</p>
}

export default function DashboardPage() {
  const { data: policies, status: policiesStatus } = useQuery({
    queryKey: ['policies'],
    queryFn: () => apiFetch<unknown[]>('/policies'),
  })

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
      <p>Stats bar, policy list, and folder grouping — coming soon.</p>
      <RunsQueryDemo />
      {renderPoliciesSection()}
    </div>
  )
}
