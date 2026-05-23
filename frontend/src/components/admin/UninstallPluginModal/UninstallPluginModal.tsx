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

// UninstallPluginModal confirms permanent removal of a plugin and all its
// instances. The instance list is shown so the operator can verify scope before
// committing. Subprocesses will be stopped and the binary directory removed.
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
  const footer = (
    <ModalFooter
      onCancel={onClose}
      onSubmit={onConfirm}
      isLoading={isPending}
      submitLabel="Uninstall plugin"
      loadingLabel="Uninstalling…"
      submitDisabled={isPending}
      variant="danger"
    />
  )

  return (
    <Modal title="Uninstall plugin" onClose={onClose} footer={footer}>
      <p className={styles.body}>
        Permanently uninstall <strong>{pluginName}</strong>? This action cannot
        be undone.
      </p>
      <p className={styles.body}>
        All subprocesses will be stopped, pending requests cancelled, OAuth
        tokens revoked, and the plugin binary directory removed.
      </p>

      {instanceNames.length > 0 && (
        <div className={styles.instanceList}>
          <p className={styles.instanceListLabel}>Instances that will be removed:</p>
          <ul className={styles.instances}>
            {instanceNames.map((name) => (
              <li key={name} className={styles.instance}>
                {name}
              </li>
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
