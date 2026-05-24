import { Modal } from '@/components/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import alertStyles from '@/styles/alerts.module.css'
import styles from './AcceptManifestModal.module.css'

export interface AcceptManifestModalProps {
  pluginName: string
  // Number of instances currently stuck on pending_manifest_approval —
  // displayed so the operator knows how many will be unblocked.
  pendingInstanceCount: number
  onClose: () => void
  onConfirm: () => void
  isPending: boolean
  error: string | null
}

// AcceptManifestModal confirms that an admin wants to commit the pending
// candidate manifest change for a plugin. The candidate is fetched server-side
// from the latest plugin_manifest_material_change audit event — the operator
// approves by clicking Accept; no manifest payload is needed in the UI.
//
// Caller owns the mutation state (isPending + error) so the modal is purely
// presentational.
export function AcceptManifestModal({
  pluginName,
  pendingInstanceCount,
  onClose,
  onConfirm,
  isPending,
  error,
}: AcceptManifestModalProps) {
  const footer = (
    <ModalFooter
      onCancel={onClose}
      onSubmit={onConfirm}
      isLoading={isPending}
      submitLabel="Accept manifest change"
      loadingLabel="Accepting…"
      submitDisabled={isPending}
    />
  )

  const instanceSummary =
    pendingInstanceCount === 1
      ? '1 instance is currently blocked'
      : `${pendingInstanceCount} instances are currently blocked`

  return (
    <Modal title="Accept pending manifest change" onClose={onClose} footer={footer}>
      <p className={styles.body}>
        A new version of <strong>{pluginName}</strong> introduced a material
        manifest change. Accepting it commits the candidate manifest as the new
        snapshot. {instanceSummary} on this change.
      </p>
      <p className={styles.detail}>
        Instances without newly-required config fields will transition back to
        healthy. Instances where the new manifest adds required fields will
        move to pending_config_migration until the missing config is supplied.
      </p>

      {error != null && (
        <div className={alertStyles.alertError} role="alert">
          {error}
        </div>
      )}
    </Modal>
  )
}
