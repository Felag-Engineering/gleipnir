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
  /**
   * When provided, deletion is blocked. Each string names a blocker
   * (e.g. "3 in-flight calls", "policy: foo"). The submit button is
   * disabled and a "Cannot delete" message is shown in place of the
   * confirm button label.
   */
  blockers?: string[]
}

// DeletePluginInstanceModal asks the admin to confirm permanent deletion of a
// plugin instance. Displays the plugin and instance names so the operator can
// verify they are removing the correct row.
//
// The caller owns the mutation state and passes isPending + error so the
// modal can remain a presentational component that is straightforward to test.
//
// Pass blockers when deletion cannot proceed (e.g. in-flight calls) to show
// a non-dismissible explanation and disable the submit button.
export function DeletePluginInstanceModal({
  pluginName,
  instanceName,
  onClose,
  onConfirm,
  isPending,
  error,
  blockers = [],
}: DeletePluginInstanceModalProps) {
  const isBlocked = blockers.length > 0

  const footer = (
    <ModalFooter
      onCancel={onClose}
      onSubmit={onConfirm}
      isLoading={isPending}
      submitLabel={isBlocked ? 'Cannot delete' : 'Delete instance'}
      loadingLabel="Deleting…"
      submitDisabled={isPending || isBlocked}
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

      {isBlocked && (
        <div className={alertStyles.alertError} role="alert">
          <p className={styles.body}>Cannot delete — resolve the following first:</p>
          <ul>
            {blockers.map((b) => (
              <li key={b}>{b}</li>
            ))}
          </ul>
        </div>
      )}

      {error != null && (
        <div className={alertStyles.alertError} role="alert">
          {error}
        </div>
      )}
    </Modal>
  )
}
