import { Modal } from '@/components/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import alertStyles from '@/styles/alerts.module.css'
import styles from './UninstallPluginModal.module.css'

export interface UninstallPluginModalProps {
  pluginName: string
  /** Names of all instances that will be stopped and removed. */
  instanceNames: string[]
  onClose: () => void
  onConfirm: () => void
  isPending: boolean
  error: string | null
}

// UninstallPluginModal confirms permanent removal of a plugin.
//
// Since issue #243, the backend requires all instances to be removed first —
// the plugin-level Remove is gated on zero instances. When instanceNames is
// non-empty the modal is in a blocked state: it explains which instances must
// be deleted first and disables the submit button.
//
// The caller owns mutation state (isPending, error) so this component is
// purely presentational and easy to test.
export function UninstallPluginModal({
  pluginName,
  instanceNames,
  onClose,
  onConfirm,
  isPending,
  error,
}: UninstallPluginModalProps) {
  const isBlocked = instanceNames.length > 0

  const footer = (
    <ModalFooter
      onCancel={onClose}
      onSubmit={onConfirm}
      isLoading={isPending}
      submitLabel={isBlocked ? 'Cannot uninstall' : 'Uninstall plugin'}
      loadingLabel="Uninstalling…"
      submitDisabled={isPending || isBlocked}
      variant="danger"
    />
  )

  return (
    <Modal title="Uninstall plugin" onClose={onClose} footer={footer}>
      <p className={styles.body}>
        Permanently uninstall <strong>{pluginName}</strong>? This action cannot
        be undone.
      </p>

      {isBlocked ? (
        <>
          <p className={styles.body}>
            All instances must be deleted before the plugin can be uninstalled.
            Delete each instance below first, then return here to complete the
            uninstall.
          </p>
          <div className={styles.instanceList}>
            <p className={styles.instanceListLabel}>Remaining instances:</p>
            <ul className={styles.instances}>
              {instanceNames.map((name) => (
                <li key={name} className={styles.instance}>
                  {name}
                </li>
              ))}
            </ul>
          </div>
        </>
      ) : (
        <p className={styles.body}>
          The plugin binary directory will be removed. There are no remaining
          instances.
        </p>
      )}

      {error != null && (
        <div className={alertStyles.alertError} role="alert">
          {error}
        </div>
      )}
    </Modal>
  )
}
