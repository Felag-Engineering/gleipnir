import { useState, type FormEvent } from 'react'
import { Modal } from '@/components/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import { Button } from '@/components/Button/Button'
import { useTestMcpConnection } from '@/hooks/mutations/servers'
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
  const testMutation = useTestMcpConnection()

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (name.trim() && url.trim()) {
      onSubmit(name.trim(), url.trim())
    }
  }

  function handleUrlChange(e: React.ChangeEvent<HTMLInputElement>) {
    setUrl(e.target.value)
    // Clear previous test result when the URL changes so stale results are not shown.
    if (testMutation.data || testMutation.isError) {
      testMutation.reset()
    }
  }

  function handleTestConnection() {
    if (url.trim()) {
      testMutation.mutate({ url: url.trim() })
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
          <div className={styles.testRow}>
            <input
              id="server-url"
              type="url"
              className={styles.input}
              placeholder="http://my-mcp-server:8080"
              value={url}
              onChange={handleUrlChange}
              required
            />
            <Button
              variant="secondary"
              size="small"
              className={styles.testButton}
              disabled={!url.trim() || testMutation.isPending}
              onClick={handleTestConnection}
            >
              {testMutation.isPending ? 'Testing...' : 'Test connection'}
            </Button>
          </div>
          {testMutation.data && (
            <div className={styles.testResult}>
              {testMutation.data.ok && testMutation.data.tool_count > 0 && (
                <div className={alertStyles.alertSuccess} role="status">
                  Connection successful — {testMutation.data.tool_count} tool(s) found
                  {testMutation.data.tools.length > 0 && (
                    <ul className={styles.toolList}>
                      {testMutation.data.tools.map((tool) => (
                        <li key={tool}>{tool}</li>
                      ))}
                    </ul>
                  )}
                </div>
              )}
              {testMutation.data.ok && testMutation.data.tool_count === 0 && (
                <div className={alertStyles.alertWarning} role="status">
                  Connection successful but no tools found
                </div>
              )}
              {!testMutation.data.ok && (
                <div className={alertStyles.alertError} role="alert">
                  {testMutation.data.error}
                </div>
              )}
            </div>
          )}
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
