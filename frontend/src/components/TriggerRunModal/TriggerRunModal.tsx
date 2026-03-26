import { useState } from 'react'
import { Modal } from '@/components/Modal/Modal'
import { Button } from '@/components/Button'
import { useTriggerPolicy } from '@/hooks/useTriggerPolicy'
import styles from './TriggerRunModal.module.css'

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
    <>
      <Button type="button" variant="ghost" onClick={onClose} disabled={isPending}>
        Cancel
      </Button>
      <Button type="submit" form="trigger-run-form" variant="primary" disabled={isPending}>
        {isPending ? 'Starting…' : 'Run now'}
      </Button>
    </>
  )

  return (
    <Modal title={`Run "${policyName}"`} onClose={onClose} footer={footer}>
      <form id="trigger-run-form" onSubmit={handleSubmit} className={styles.form}>
        <div className={styles.field}>
          <label htmlFor="trigger-message" className={styles.label}>
            Message <span className={styles.optional}>(optional)</span>
          </label>
          <textarea
            id="trigger-message"
            className={styles.textarea}
            value={message}
            onChange={(e) => setMessage(e.target.value)}
            placeholder="Provide context or instructions for this run…"
            rows={4}
          />
        </div>
        {error && (
          <p className={styles.errorMsg}>{error.message}</p>
        )}
      </form>
    </Modal>
  )
}
