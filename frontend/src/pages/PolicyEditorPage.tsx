import { useParams } from 'react-router-dom'

export default function PolicyEditorPage() {
  const { id } = useParams<{ id: string }>()

  return (
    <div>
      <h1>{id ? `Edit Policy: ${id}` : 'New Policy'}</h1>
      <p>Dual-mode YAML/form policy editor — coming soon.</p>
    </div>
  )
}
