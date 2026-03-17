import { Modal } from '@/components/Modal'
import type { ApiError } from '@/api/fetch'
import styles from './DeleteServerModal.module.css'

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
      <button type="button" className={styles.cancelBtn} onClick={onClose} disabled={isPending}>
        Cancel
      </button>
      <button
        type="button"
        className={styles.deleteBtn}
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
      </button>
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
          <div className={styles.errorMsg} role="alert">
            {error.detail ?? error.message}
          </div>
        )}
      </div>
    </Modal>
  )
}
