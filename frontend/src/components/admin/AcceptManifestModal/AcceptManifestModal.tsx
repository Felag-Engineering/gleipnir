import type { ApiPluginV2Consent } from '@/api/types'
import { Modal } from '@/components/Modal'
import { PluginV2Consent } from '@/components/admin/PluginReviewCard/PluginV2Consent'
import { ModalFooter } from '@/components/ModalFooter'
import alertStyles from '@/styles/alerts.module.css'
import styles from './AcceptManifestModal.module.css'

export interface AcceptManifestModalProps {
  pluginName: string
  // Number of instances currently stuck on pending_manifest_approval —
  // displayed so the operator knows how many will be unblocked.
  pendingInstanceCount: number
  // The blocked v2 candidate to review before accepting; absent for a v1 plugin.
  pendingConsent?: ApiPluginV2Consent
  // True while the plugin detail (which carries the candidate) is loading or
  // failed: acceptance is held until the admin can see what they accept.
  detailUnavailable?: boolean
  onClose: () => void
  onConfirm: () => void
  isPending: boolean
  error: string | null
}

// AcceptManifestModal confirms that an admin wants to commit the pending
// candidate manifest change for a plugin. A v2 candidate is shown in full
// (image digest, egress, resources, ...) before Accept, and the caller sends
// back the hash of what was shown.
//
// Caller owns the mutation state (isPending + error) so the modal is purely
// presentational.
export function AcceptManifestModal({
  pluginName,
  pendingInstanceCount,
  pendingConsent,
  detailUnavailable = false,
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
      submitDisabled={isPending || detailUnavailable}
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

      {pendingConsent && <PluginV2Consent consent={pendingConsent} />}

      {error != null && (
        <div className={alertStyles.alertError} role="alert">
          {error}
        </div>
      )}
    </Modal>
  )
}
