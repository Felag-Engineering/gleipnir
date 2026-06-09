import { Modal } from '@/components/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import alertStyles from '@/styles/alerts.module.css'
import styles from './RejectPluginModal.module.css'

export interface RejectPluginModalProps {
  pluginName: string
  onClose: () => void
  onConfirm: () => void
  isPending: boolean
  error: string | null
}

// RejectPluginModal confirms permanent rejection (and deletion) of a
// pending-review plugin. Warns the admin that rejection deletes the plugin row,
// but that the bundle can be re-dropped to re-install.
//
// The caller owns mutation state (isPending, error) so this component is
// purely presentational and easy to test.
export function RejectPluginModal({
  pluginName,
  onClose,
  onConfirm,
  isPending,
  error,
}: RejectPluginModalProps) {
  const footer = (
    <ModalFooter
      onCancel={onClose}
      onSubmit={onConfirm}
      isLoading={isPending}
      submitLabel="Reject plugin"
      loadingLabel="Rejecting…"
      submitDisabled={isPending}
      variant="danger"
    />
  )

  return (
    <Modal title="Reject plugin" onClose={onClose} footer={footer}>
      <p className={styles.body}>
        Reject and delete <strong>{pluginName}</strong>? The plugin will be
        removed and no instances will be created.
      </p>
      <p className={styles.body}>
        If you change your mind, drop the bundle back into{' '}
        <code className={styles.code}>/plugins</code> to re-install it.
      </p>

      {error != null && (
        <div className={alertStyles.alertError} role="alert">
          {error}
        </div>
      )}
    </Modal>
  )
}
