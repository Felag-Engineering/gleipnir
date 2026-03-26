import { useState, type FormEvent } from 'react'
import { Modal } from '@/components/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import type { ApiError } from '@/api/fetch'
import styles from './AddServerModal.module.css'
import formStyles from '@/styles/forms.module.css'
import alertStyles from '@/styles/alerts.module.css'

interface Props {
  onClose: () => void
  onSubmit: (name: string, url: string) => void
  isPending: boolean
  error: ApiError | null
  discoveryWarning?: string | null
}

export function AddServerModal({ onClose, onSubmit, isPending, error, discoveryWarning }: Props) {
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (name.trim() && url.trim()) {
      onSubmit(name.trim(), url.trim())
    }
  }

  const footer = (
    <ModalFooter
      onCancel={onClose}
      formId="add-server-form"
      isLoading={isPending}
      submitLabel="Add MCP server"
      loadingLabel="Adding…"
      submitDisabled={!name.trim() || !url.trim()}
    />
  )

  return (
    <Modal title="Add MCP server" onClose={onClose} footer={footer}>
      <form id="add-server-form" onSubmit={handleSubmit} className={formStyles.form}>
        <div className={formStyles.field}>
          <label htmlFor="server-name" className={formStyles.labelMono}>Name</label>
          <input
            id="server-name"
            type="text"
            className={styles.input}
            placeholder="e.g. kubectl-mcp"
            value={name}
            onChange={(e) => setName(e.target.value)}
            autoFocus
            required
          />
        </div>
        <div className={formStyles.field}>
          <label htmlFor="server-url" className={formStyles.labelMono}>URL</label>
          <input
            id="server-url"
            type="url"
            className={styles.input}
            placeholder="http://my-mcp-server:8080"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            required
          />
        </div>
        {error && (
          <div className={alertStyles.alertError} role="alert">
            {error.message}
          </div>
        )}
        {discoveryWarning && (
          <div className={alertStyles.alertWarning} role="status">
            Server registered, but tool discovery failed: {discoveryWarning}. You can retry with the Discover button.
          </div>
        )}
      </form>
    </Modal>
  )
}
