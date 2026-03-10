import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '../api/fetch'

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
  return (
    <div>
      <h1>Dashboard</h1>
      <p>Stats bar, policy list, and folder grouping — coming soon.</p>
      <RunsQueryDemo />
    </div>
  )
}
