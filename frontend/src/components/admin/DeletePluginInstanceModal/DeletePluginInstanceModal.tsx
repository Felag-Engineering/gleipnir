import { Modal } from '@/components/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import alertStyles from '@/styles/alerts.module.css'
import styles from './DeletePluginInstanceModal.module.css'

export interface DeletePluginInstanceModalProps {
  pluginName: string
  instanceName: string
  onClose: () => void
  onConfirm: () => void
  isPending: boolean
  error: string | null
}

// DeletePluginInstanceModal asks the admin to confirm permanent deletion of a
// plugin instance. Displays the plugin and instance names so the operator can
// verify they are removing the correct row.
//
// The caller owns the mutation state and passes isPending + error so the
// modal can remain a presentational component that is straightforward to test.
export function DeletePluginInstanceModal({
  pluginName,
  instanceName,
  onClose,
  onConfirm,
  isPending,
  error,
}: DeletePluginInstanceModalProps) {
  const footer = (
    <ModalFooter
      onCancel={onClose}
      onSubmit={onConfirm}
      isLoading={isPending}
      submitLabel="Delete instance"
      loadingLabel="Deleting…"
      submitDisabled={isPending}
      variant="danger"
    />
  )

  return (
    <Modal title="Delete plugin instance" onClose={onClose} footer={footer}>
      <p className={styles.body}>
        Permanently delete the <strong>{instanceName}</strong> instance of{' '}
        <strong>{pluginName}</strong>? This action cannot be undone.
      </p>
      <p className={styles.body}>
        The instance subprocess will be stopped, any pending requests will be
        cancelled, and OAuth tokens will be revoked. The plugin itself is not
        removed — other instances are unaffected.
      </p>

      {error != null && (
        <div className={alertStyles.alertError} role="alert">
          {error}
        </div>
      )}
    </Modal>
  )
}
