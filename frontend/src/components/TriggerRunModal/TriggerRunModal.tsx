import { useState } from 'react'
import { Modal } from '@/components/Modal/Modal'
import { ModalFooter } from '@/components/ModalFooter'
import { useTriggerPolicy } from '@/hooks/mutations/policies'
import formStyles from '@/styles/forms.module.css'
import alertStyles from '@/styles/alerts.module.css'

interface TriggerRunModalProps {
  policyId: string
  policyName: string
  onClose: () => void
  onSuccess: (runId: string) => void
}

export function TriggerRunModal({ policyId, policyName, onClose, onSuccess }: TriggerRunModalProps) {
  const [message, setMessage] = useState('')
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
        {error && (
          <div className={alertStyles.alertError} role="alert">{error.message}</div>
        )}
      </form>
    </Modal>
  )
}
