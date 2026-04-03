import { useState } from 'react'
import { Button } from '@/components/Button/Button'
import { useSubmitFeedback } from '@/hooks/mutations/runs'
import { useCurrentUser } from '@/hooks/queries/users'
import styles from './FeedbackActions.module.css'

interface Props {
  runId: string
  runStatus: string
  feedbackId?: string
}

export function FeedbackActions({ runId, runStatus, feedbackId }: Props) {
  const { data: user } = useCurrentUser()
  const submitFeedback = useSubmitFeedback()
  const [response, setResponse] = useState('')

  if (runStatus !== 'waiting_for_feedback') {
    return null
  }

  // Operators and admins can respond to feedback requests.
  const canRespond = user?.roles.some((r) => r === 'operator' || r === 'approver' || r === 'admin')
  if (!canRespond) {
    return null
  }

  const handleSubmit = () => {
    if (!response.trim()) return
    submitFeedback.mutate({ runId, response, feedbackId })
  }

  return (
    <div className={styles.container}>
      <textarea
        className={styles.textarea}
        placeholder="Enter your response for the agent…"
        value={response}
        onChange={(e) => setResponse(e.target.value)}
        disabled={submitFeedback.isPending}
        rows={3}
      />
      <div className={styles.actions}>
        <Button
          variant="primary"
          size="small"
          disabled={submitFeedback.isPending || !response.trim()}
          onClick={handleSubmit}
        >
          Submit Response
        </Button>
      </div>
      {submitFeedback.isError && (
        <p className={styles.error}>
          {submitFeedback.error instanceof Error
            ? submitFeedback.error.message
            : 'Failed to submit response'}
        </p>
      )}
    </div>
  )
}
