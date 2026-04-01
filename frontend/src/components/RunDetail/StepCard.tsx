import { CollapsibleJSON } from '@/components/CollapsibleJSON'
import { ApprovalActions } from './ApprovalActions'
import { FeedbackActions } from './FeedbackActions'
import type { ParsedStep, GrantedToolEntry } from './types'
import styles from './StepCard.module.css'

interface Props {
  step: ParsedStep
  toolRoleMap: Map<string, GrantedToolEntry['Role']>
  runId: string
  runStatus: string
}

function StepIcon({ type, role }: { type: string; role?: GrantedToolEntry['Role'] }) {
  const cls = [styles.icon]
  if (type === 'thought') cls.push(styles.iconThought)
  else if (type === 'tool_call' && role === 'tool') cls.push(styles.iconTool)
  else if (type === 'tool_result') cls.push(styles.iconResult)
  else if (type === 'error') cls.push(styles.iconError)
  else if (type === 'complete') cls.push(styles.iconComplete)
  else if (type === 'approval_request') cls.push(styles.iconApproval)
  else if (type === 'feedback_request' || type === 'feedback_response') cls.push(styles.iconFeedback)
  else cls.push(styles.iconDefault)
  return <span className={cls.join(' ')} aria-hidden="true" />
}

export function StepCard({ step, toolRoleMap, runId, runStatus }: Props) {
  if (step.type === 'thought') {
    return (
      <div className={styles.card}>
        <div className={styles.iconCol}>
          <StepIcon type="thought" />
        </div>
        <div className={styles.body}>
          <span className={styles.typeLabel}>Thought</span>
          <p className={styles.thoughtText}>{step.content.text}</p>
        </div>
      </div>
    )
  }

  if (step.type === 'tool_call') {
    return (
      <div className={styles.card}>
        <div className={styles.iconCol}>
          <StepIcon type="tool_call" role="tool" />
        </div>
        <div className={styles.body}>
          <div className={styles.row}>
            <span className={`${styles.typeLabel} ${styles.toolLabel}`}>
              tool call
            </span>
            <code className={styles.toolName}>{step.content.tool_name}</code>
          </div>
          <CollapsibleJSON value={step.content.input} />
        </div>
      </div>
    )
  }

  if (step.type === 'tool_result') {
    const isError = step.content.is_error
    let parsed: unknown
    try {
      parsed = JSON.parse(step.content.output)
    } catch {
      parsed = step.content.output
    }
    return (
      <div className={styles.card}>
        <div className={styles.iconCol}>
          <StepIcon type="tool_result" />
        </div>
        <div className={styles.body}>
          <div className={styles.row}>
            <span className={`${styles.typeLabel} ${isError ? styles.errorLabel : styles.resultLabel}`}>
              {isError ? 'result (error)' : 'result'}
            </span>
            <code className={styles.toolName}>{step.content.tool_name}</code>
          </div>
          <CollapsibleJSON value={parsed} />
        </div>
      </div>
    )
  }

  if (step.type === 'error') {
    return (
      <div className={`${styles.card} ${styles.cardError}`}>
        <div className={styles.iconCol}>
          <StepIcon type="error" />
        </div>
        <div className={styles.body}>
          <span className={`${styles.typeLabel} ${styles.errorLabel}`}>Error</span>
          <pre className={styles.errorText}>{step.content.message}</pre>
        </div>
      </div>
    )
  }

  if (step.type === 'complete') {
    return (
      <div className={styles.card}>
        <div className={styles.iconCol}>
          <StepIcon type="complete" />
        </div>
        <div className={styles.body}>
          <span className={`${styles.typeLabel} ${styles.completeLabel}`}>Complete</span>
          {step.content.message && (
            <p className={styles.bodyText}>{step.content.message}</p>
          )}
        </div>
      </div>
    )
  }

  if (step.type === 'approval_request') {
    return (
      <div className={styles.card}>
        <div className={styles.iconCol}>
          <StepIcon type="approval_request" />
        </div>
        <div className={styles.body}>
          <span className={`${styles.typeLabel} ${styles.approvalLabel}`}>Approval requested</span>
          <code className={styles.toolName}>{step.content.tool}</code>
          <ApprovalActions runId={runId} runStatus={runStatus} />
        </div>
      </div>
    )
  }

  if (step.type === 'feedback_request') {
    // Split message on first \n\n to extract reason (headline) and optional context (body).
    // If no \n\n, the entire message is the reason. Old steps without message fall back to the tool name.
    const message = step.content.message ?? step.content.tool
    const separatorIndex = message.indexOf('\n\n')
    const reason = separatorIndex !== -1 ? message.slice(0, separatorIndex) : message
    const context = separatorIndex !== -1 ? message.slice(separatorIndex + 2) : undefined

    return (
      <div className={styles.card}>
        <div className={styles.iconCol}>
          <StepIcon type="feedback_request" />
        </div>
        <div className={styles.body}>
          <span className={`${styles.typeLabel} ${styles.feedbackLabel}`}>Feedback requested</span>
          <p className={styles.feedbackReason}>{reason}</p>
          {context && (
            <p className={styles.bodyText}>{context}</p>
          )}
          <FeedbackActions runId={runId} runStatus={runStatus} feedbackId={step.content.feedback_id} />
        </div>
      </div>
    )
  }

  if (step.type === 'feedback_response') {
    return (
      <div className={styles.card}>
        <div className={styles.iconCol}>
          <StepIcon type="feedback_response" />
        </div>
        <div className={styles.body}>
          <span className={`${styles.typeLabel} ${styles.feedbackLabel}`}>Feedback received</span>
          {step.content.response && (
            <p className={styles.bodyText}>{step.content.response}</p>
          )}
        </div>
      </div>
    )
  }

  // unknown
  return (
    <div className={styles.card}>
      <div className={styles.iconCol}>
        <StepIcon type="unknown" />
      </div>
      <div className={styles.body}>
        <span className={styles.typeLabel}>{step.raw.type}</span>
        <CollapsibleJSON value={step.content} />
      </div>
    </div>
  )
}
