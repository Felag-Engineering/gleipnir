import { Modal } from '@/components/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import alertStyles from '@/styles/alerts.module.css'
import styles from './ClearCredentialsModal.module.css'

export interface ClearCredentialsModalProps {
  onClose: () => void
  onConfirm: () => void
  isPending: boolean
  error: string | null
}

// ClearCredentialsModal asks the admin to confirm clearing an instance's stored
// credentials. Clearing is destructive and has no undo — for an OAuth instance it
// wipes the token and forces a full re-authorization. The neighboring
// "Delete instance" action is already gated by a confirmation modal, so this keeps
// the two destructive actions consistent (see issue #659).
//
// The caller owns the mutation state and passes isPending + error so the modal can
// remain a presentational component that is straightforward to test — mirrors
// DeletePluginInstanceModal.
export function ClearCredentialsModal({
  onClose,
  onConfirm,
  isPending,
  error,
}: ClearCredentialsModalProps) {
  const footer = (
    <ModalFooter
      onCancel={onClose}
      onSubmit={onConfirm}
      isLoading={isPending}
      submitLabel="Clear credentials"
      loadingLabel="Clearing…"
      submitDisabled={isPending}
      variant="danger"
    />
  )

  return (
    <Modal title="Clear credentials" onClose={onClose} footer={footer}>
      <p className={styles.body}>
        This removes the stored credentials. You'll need to re-authorize before this
        instance can call its tools/channels again.
      </p>
      <p className={styles.body}>This action cannot be undone.</p>

      {error != null && (
        <div className={alertStyles.alertError} role="alert">
          {error}
        </div>
      )}
    </Modal>
  )
}
