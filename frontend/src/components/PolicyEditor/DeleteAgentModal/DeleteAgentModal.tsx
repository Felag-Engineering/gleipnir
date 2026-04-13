import { useState } from 'react'
import { Modal } from '@/components/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import { useRuns } from '@/hooks/queries/runs'
import type { ApiError } from '@/api/fetch'
import styles from './DeleteAgentModal.module.css'
import alertStyles from '@/styles/alerts.module.css'

// Runs threshold above which we require typing the agent name to confirm.
const CONFIRM_NAME_THRESHOLD = 5

interface Props {
  policyId: string
  policyName: string
  onClose: () => void
  onConfirm: () => void
  isPending: boolean
  error: ApiError | null
}

export function DeleteAgentModal({ policyId, policyName, onClose, onConfirm, isPending, error }: Props) {
  const [confirmName, setConfirmName] = useState('')

  const { total: runCount, status: runsStatus } = useRuns({ policy_id: policyId, limit: 1 })

  const requiresNameConfirm = runsStatus === 'success' && runCount >= CONFIRM_NAME_THRESHOLD
  const nameMatches = confirmName === policyName
  const submitDisabled = requiresNameConfirm && !nameMatches

  const footer = (
    <ModalFooter
      onCancel={onClose}
      onSubmit={onConfirm}
      isLoading={isPending}
      submitLabel="Delete agent"
      loadingLabel="Deleting..."
      variant="danger"
      submitDisabled={submitDisabled}
    />
  )

  return (
    <Modal title="Delete agent?" onClose={onClose} footer={footer}>
      <div className={styles.body}>
        <p className={styles.message}>
          This will permanently delete <strong className={styles.name}>{policyName}</strong>
          {runsStatus === 'success' && runCount > 0 && (
            <> and all <strong>{runCount} {runCount === 1 ? 'run' : 'runs'}</strong> in its audit trail</>
          )}
          {runsStatus === 'success' && runCount === 0 && (
            <> (no runs in audit trail)</>
          )}
          . This cannot be undone.
        </p>

        {requiresNameConfirm && (
          <div className={styles.confirmSection}>
            <label htmlFor="confirm-name" className={styles.confirmLabel}>
              Type <strong className={styles.name}>{policyName}</strong> to confirm:
            </label>
            <input
              id="confirm-name"
              type="text"
              className={styles.confirmInput}
              value={confirmName}
              onChange={e => setConfirmName(e.target.value)}
              autoComplete="off"
              spellCheck={false}
            />
          </div>
        )}

        {error && (
          <div className={alertStyles.alertError} role="alert">
            {error.detail ?? error.message}
          </div>
        )}
      </div>
    </Modal>
  )
}
