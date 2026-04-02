import { FeedbackActions } from './FeedbackActions'
import { useCountdown } from '@/hooks/useCountdown'
import type { ParsedStep, FeedbackRequestContent } from './types'
import styles from './FeedbackBlock.module.css'

interface Props {
  step: (ParsedStep & { type: 'feedback_request' }) | (ParsedStep & { type: 'feedback_response' })
  runId: string
  runStatus: string
}

// FeedbackRequestStep is extracted as its own component so that useCountdown
// can be called unconditionally at the top level (React rules of hooks prohibit
// calling hooks inside conditional branches of a parent component).
function FeedbackRequestStep({ content, runId, runStatus }: { content: FeedbackRequestContent; runId: string; runStatus: string }) {
  // Split message on first \n\n to extract reason (headline) and optional context (body).
  // If no \n\n, the entire message is the reason. Old steps without message fall back to the tool name.
  const message = content.message ?? content.tool
  const separatorIndex = message.indexOf('\n\n')
  const reason = separatorIndex !== -1 ? message.slice(0, separatorIndex) : message
  const context = separatorIndex !== -1 ? message.slice(separatorIndex + 2) : undefined

  // Only show the countdown while the run is still waiting for a response.
  // Once resolved or timed out, the countdown is no longer meaningful.
  const countdown = useCountdown(runStatus === 'waiting_for_feedback' ? content.expires_at : undefined)

  return (
    <div className={styles.block}>
      <div className={styles.header}>
        <span className={styles.dot} aria-hidden="true" />
        <span className={styles.label}>Feedback requested</span>
        {countdown && (
          <div className={`${styles.timer} ${countdown.urgent ? styles.timerUrgent : styles.timerNormal}`}>
            <svg role="img" aria-label="Time remaining" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <circle cx="12" cy="12" r="10" /><polyline points="12 6 12 12 16 14" />
            </svg>
            <span>{countdown.str}</span>
          </div>
        )}
      </div>
      <div className={styles.content}>
        <p className={styles.reason}>{reason}</p>
        {context && <p className={styles.body}>{context}</p>}
        <FeedbackActions runId={runId} runStatus={runStatus} feedbackId={content.feedback_id} />
      </div>
    </div>
  )
}

export function FeedbackBlock({ step, runId, runStatus }: Props) {
  if (step.type === 'feedback_request') {
    return <FeedbackRequestStep content={step.content} runId={runId} runStatus={runStatus} />
  }

  return (
    <div className={styles.block}>
      <div className={styles.header}>
        <span className={styles.dot} aria-hidden="true" />
        <span className={styles.label}>Feedback received</span>
      </div>
      {step.content.response && (
        <div className={styles.content}>
          <p className={styles.body}>{step.content.response}</p>
        </div>
      )}
    </div>
  )
}
