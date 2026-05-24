import { useState } from 'react'
import { Modal } from '@/components/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import { useAcceptPluginNewKey } from '@/hooks/mutations/plugins'
import { extractErrorMessage } from '@/api/fetch'
import styles from './AcceptNewKeyModal.module.css'

interface AcceptNewKeyModalProps {
  pluginId: string
  /** 8-byte hex key ID of the currently-trusted pubkey. */
  oldPubkeyFingerprint: string
  /** 8-byte hex key ID of the new candidate pubkey. */
  newPubkeyFingerprint: string
  /** Base64-encoded Minisign signing.pub bytes from the audit event. Passed as-is to the API. */
  candidatePubkeyB64: string
  onClose: () => void
}

// AcceptNewKeyModal lets an admin approve a new signing key for a plugin that
// is blocked in pending_key_approval state. The modal shows both key fingerprints
// so the admin can verify the new key out-of-band before accepting.
//
// Wire this modal to the PluginHealthChip's onClick when state === 'pending_key_approval'.
// Source the props from the plugin_pubkey_mismatch audit event payload.
export function AcceptNewKeyModal({
  pluginId,
  oldPubkeyFingerprint,
  newPubkeyFingerprint,
  candidatePubkeyB64,
  onClose,
}: AcceptNewKeyModalProps) {
  const [error, setError] = useState<string | null>(null)
  const mutation = useAcceptPluginNewKey()

  function handleAccept() {
    setError(null)
    mutation.mutate(
      { pluginId, candidatePubkey: candidatePubkeyB64 },
      {
        onSuccess: onClose,
        onError: (err) => {
          setError(extractErrorMessage(err))
        },
      },
    )
  }

  const footer = (
    <ModalFooter
      onCancel={onClose}
      onSubmit={handleAccept}
      isLoading={mutation.isPending}
      submitLabel="Accept new key"
      loadingLabel="Accepting…"
      submitDisabled={mutation.isPending}
      variant="danger"
    />
  )

  return (
    <Modal title="Accept new signing key" onClose={onClose} footer={footer}>
      <div className={styles.warning}>
        <strong>This plugin was updated with a different signing key.</strong>{' '}
        Verify the new key fingerprint matches what you expect before accepting.
        Accepting a key you do not recognise may allow untrusted code to run.
      </div>

      <div className={styles.keyGrid}>
        <div className={styles.keyRow}>
          <span className={styles.keyLabel}>Current trusted key</span>
          <code className={styles.fingerprint}>{oldPubkeyFingerprint || '(none — previously unsigned)'}</code>
        </div>
        <div className={styles.keyRow}>
          <span className={styles.keyLabel}>New candidate key</span>
          <code className={styles.fingerprint}>{newPubkeyFingerprint}</code>
        </div>
      </div>

      {error != null && (
        <div className={styles.errorBanner} role="alert">
          {error}
        </div>
      )}
    </Modal>
  )
}
