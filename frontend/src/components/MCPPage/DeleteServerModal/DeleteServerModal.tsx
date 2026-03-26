import { Modal } from '@/components/Modal'
import { Button } from '@/components/Button'
import type { ApiError } from '@/api/fetch'
import styles from './DeleteServerModal.module.css'
import alertStyles from '@/styles/alerts.module.css'

interface Props {
  serverName: string
  toolCount: number
  onClose: () => void
  onConfirm: () => void
  isPending: boolean
  error: ApiError | null
}

export function DeleteServerModal({ serverName, toolCount, onClose, onConfirm, isPending, error }: Props) {
  const footer = (
    <>
      <Button type="button" variant="ghost" onClick={onClose} disabled={isPending}>
        Cancel
      </Button>
      <Button
        type="button"
        variant="danger"
        onClick={onConfirm}
        disabled={isPending}
      >
        {isPending ? (
          <>
            <span className={styles.spinner} aria-hidden="true" />
            Deleting…
          </>
        ) : (
          'Delete MCP server'
        )}
      </Button>
    </>
  )

  return (
    <Modal title="Delete MCP server" onClose={onClose} footer={footer}>
      <div className={styles.body}>
        <p className={styles.message}>
          Delete <strong className={styles.name}>{serverName}</strong>?
          {toolCount > 0 && (
            <> This will also remove {toolCount} {toolCount === 1 ? 'tool' : 'tools'}.</>
          )}
        </p>
        <p className={styles.warning}>
          Any policies referencing tools from this server will fail to run.
        </p>
        {error && (
          <div className={alertStyles.alertError} role="alert">
            {error.detail ?? error.message}
          </div>
        )}
      </div>
    </Modal>
  )
}
