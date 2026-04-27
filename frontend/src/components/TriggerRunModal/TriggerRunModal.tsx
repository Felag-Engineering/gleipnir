import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Modal } from '@/components/Modal/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import { ApiError } from '@/api/fetch'
import { useTriggerPolicy } from '@/hooks/mutations/policies'
import { formatTimeAgo } from '@/utils/format'
import formStyles from '@/styles/forms.module.css'
import alertStyles from '@/styles/alerts.module.css'
import styles from './TriggerRunModal.module.css'

interface TriggerRunModalProps {
  policyId: string
  policyName: string
  onClose: () => void
  onSuccess: (runId: string) => void
  initialMessage?: string
  policyUpdatedAt?: string | null
}

export function TriggerRunModal({ policyId, policyName, onClose, onSuccess, initialMessage, policyUpdatedAt }: TriggerRunModalProps) {
  const [message, setMessage] = useState(initialMessage ?? '')
  const { mutate, isPending, error } = useTriggerPolicy()

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    mutate(
      { policyId, message: message.trim() || undefined },
      {
        onSuccess: (data) => {
          onSuccess(data.run_id)
        },
      },
    )
  }

  const footer = (
    <ModalFooter
      onCancel={onClose}
      formId="trigger-run-form"
      isLoading={isPending}
      submitLabel="Run now"
      loadingLabel="Starting…"
    />
  )

  return (
    <Modal title={`Run "${policyName}"`} onClose={onClose} footer={footer}>
      <form id="trigger-run-form" onSubmit={handleSubmit} className={formStyles.form}>
        <div className={formStyles.field}>
          <label htmlFor="trigger-message" className={formStyles.label}>
            Message <span className={formStyles.optional}>(optional)</span>
          </label>
          <textarea
            id="trigger-message"
            className={formStyles.textarea}
            value={message}
            onChange={(e) => setMessage(e.target.value)}
            placeholder="Provide context or instructions for this run…"
            rows={4}
          />
        </div>
        {policyUpdatedAt && (
          <p className={styles.editedNote}>
            Policy last edited {formatTimeAgo(policyUpdatedAt)}
          </p>
        )}
        {error && (
          <div className={alertStyles.alertError} role="alert">
            <div>{error.message}</div>
            {error instanceof ApiError && error.detail && (
              <div className={styles.errorDetail}>{error.detail}</div>
            )}
            {error instanceof ApiError && error.runId && (
              <Link
                to={`/runs/${encodeURIComponent(error.runId)}`}
                className={styles.errorRunLink}
                onClick={onClose}
              >
                View failed run →
              </Link>
            )}
          </div>
        )}
      </form>
    </Modal>
  )
}
