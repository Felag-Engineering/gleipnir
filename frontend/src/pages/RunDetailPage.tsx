import { useParams } from 'react-router-dom'

export default function RunDetailPage() {
  const { id } = useParams<{ id: string }>()

  return (
    <div>
      <h1>Run: {id}</h1>
      <p>Reasoning timeline with live SSE updates — coming soon.</p>
    </div>
  )
}
