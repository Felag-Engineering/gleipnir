import { useState } from 'react'
import type { ApiMcpServer } from '@/api/types'
import { useUpdateMcpServer } from '@/hooks/mutations/servers'
import { formatCallTimeout, parseCallTimeoutInput } from '../callTimeout'
import styles from './CallTimeoutSection.module.css'

interface Props {
  server: ApiMcpServer
}

// CallTimeoutSection shows a server's effective MCP call timeout (issue
// #939) and, for non-managed servers, lets an operator set, replace, or
// clear a per-server override. It owns its own useUpdateMcpServer mutation
// rather than sharing one with the rest of ServerDetailModal, mirroring
// CaCertificateSection — name/url and the call timeout are edited
// independently.
export function CallTimeoutSection({ server }: Props) {
  // A managed entry's endpoint belongs to the plugin lifecycle, the same
  // reason ServerDetailModal hides the auth-header editor and
  // CaCertificateSection hides its editor for it.
  const isManaged = server.trust_tier === 'managed'
  const hasOverride = server.call_timeout_seconds != null && server.call_timeout_seconds > 0

  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState(hasOverride ? String(server.call_timeout_seconds) : '')
  const [error, setError] = useState<string | null>(null)

  const updateMutation = useUpdateMcpServer()

  function openEditor() {
    setValue(hasOverride ? String(server.call_timeout_seconds) : '')
    setError(null)
    setEditing(true)
  }

  function handleSave() {
    const { value: parsed, error: parseError } = parseCallTimeoutInput(value)
    if (parseError) {
      setError(parseError)
      return
    }
    setError(null)
    updateMutation.mutate(
      { id: server.id, name: server.name, url: server.url, call_timeout_seconds: parsed ?? 0 },
      {
        onSuccess: () => setEditing(false),
        onError: (err) => setError(err.detail ?? err.message),
      },
    )
  }

  function handleUseDefault() {
    setError(null)
    updateMutation.mutate(
      { id: server.id, name: server.name, url: server.url, call_timeout_seconds: 0 },
      {
        onSuccess: () => {
          setValue('')
          setEditing(false)
        },
        onError: (err) => setError(err.detail ?? err.message),
      },
    )
  }

  return (
    <div className={styles.section}>
      <div className={styles.title}>Call timeout</div>
      <div className={styles.value}>{formatCallTimeout(server)}</div>

      {!isManaged && !editing && (
        <div className={styles.actions}>
          <button type="button" className={styles.editBtn} onClick={openEditor}>
            {hasOverride ? 'Edit' : 'Set override'}
          </button>
          {hasOverride && (
            <button
              type="button"
              className={styles.removeBtn}
              onClick={handleUseDefault}
              disabled={updateMutation.isPending}
            >
              Use default
            </button>
          )}
        </div>
      )}

      {!isManaged && editing && (
        <div className={styles.editor}>
          <input
            type="number"
            inputMode="numeric"
            min={1}
            max={600}
            step={1}
            className={styles.input}
            placeholder="Default"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            aria-label="Call timeout seconds"
          />
          <p className={styles.hint}>
            Raise it for servers whose tool calls legitimately run long (1–600 seconds). Leave
            blank and save to use the instance default.
          </p>
          {error && <div className={styles.error}>{error}</div>}
          <div className={styles.editorFooter}>
            <button
              type="button"
              className={styles.cancelBtn}
              onClick={() => {
                setEditing(false)
                setError(null)
              }}
            >
              Cancel
            </button>
            <button
              type="button"
              className={styles.saveBtn}
              onClick={handleSave}
              disabled={updateMutation.isPending}
            >
              {updateMutation.isPending ? 'Saving…' : 'Save'}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
