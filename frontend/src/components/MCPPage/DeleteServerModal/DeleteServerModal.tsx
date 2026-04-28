import { Modal } from '@/components/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import type { ApiError } from '@/api/fetch'
import styles from './DeleteServerModal.module.css'
import alertStyles from '@/styles/alerts.module.css'

interface Props {
  serverName: string
  toolCount: number
  onClose: () => void
  onConfirm: (force: boolean) => void
  isPending: boolean
  error: ApiError | null
}

export function DeleteServerModal({ serverName, toolCount, onClose, onConfirm, isPending, error }: Props) {
  // A 409 means the server is in use by one or more policies. The handler
  // returns the conflicting policy names in error.detail. After the user has
  // seen them, we let them retry with force=true to bypass the check.
  const inUseConflict = error?.status === 409
  const submitLabel = inUseConflict ? 'Force delete' : 'Delete MCP server'
  const loadingLabel = inUseConflict ? 'Force deleting…' : 'Deleting…'

  const footer = (
    <ModalFooter
      onCancel={onClose}
      onSubmit={() => onConfirm(inUseConflict)}
      isLoading={isPending}
      submitLabel={submitLabel}
      loadingLabel={loadingLabel}
      variant="danger"
    />
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
          Any agents referencing tools from this server will fail to run.
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
