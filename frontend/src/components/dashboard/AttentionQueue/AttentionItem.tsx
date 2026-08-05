import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { useApproveRun } from '@/hooks/mutations/runs'
import { useCountdown } from '@/hooks/useCountdown'
import type { AttentionItem as AttentionItemType } from '@/hooks/useAttentionItems'
import styles from './AttentionQueue.module.css'

interface AttentionItemProps {
  item: AttentionItemType
  onDismiss: (runId: string) => void
}

// TypeBadge renders the colored type label pill for an attention item.
function TypeBadge({ type }: { type: string }) {
  const classMap: Record<string, string> = {
    approval: styles.badgeApproval,
    feedback: styles.badgeFeedback,
    tool_input: styles.badgeToolInput,
    failure: styles.badgeFailure,
  }
  const labelMap: Record<string, string> = {
    approval: 'APPROVAL',
    feedback: 'QUESTION',
    // The UI term stays "Feedback request" for the operator-facing vocabulary
    // (#656); the badge says what the ask IS, which is the thing an operator
    // triages on. Permission vs information is the §6.1 split.
    tool_input: 'TOOL ASK',
    failure: 'FAILED',
  }
  return (
    <span className={`${styles.badge} ${classMap[type] ?? ''}`}>
      {labelMap[type] ?? type.toUpperCase()}
    </span>
  )
}

// CountdownDisplay shows remaining time until expires_at, turning red when urgent.
// Using useCountdown here (rather than a bare formatCountdown call) gives each
// item its own 1-second interval, so the displayed time updates in real time.
function CountdownDisplay({ expiresAt }: { expiresAt: string }) {
  const countdown = useCountdown(expiresAt)
  if (!countdown) return null
  return (
    <span className={`${styles.countdown} ${countdown.urgent ? styles.countdownUrgent : ''}`}>
      {countdown.str}
    </span>
  )
}

export function AttentionItem({ item, onDismiss }: AttentionItemProps) {
  const navigate = useNavigate()
  const approveRun = useApproveRun()
  const [dismissed, setDismissed] = useState(false)

  const accentClassMap: Record<string, string> = {
    approval: styles.accentApproval,
    feedback: styles.accentFeedback,
    tool_input: styles.accentToolInput,
    failure: styles.accentFailure,
  }

  if (dismissed) {
    return null
  }

  function handleApprove() {
    approveRun.mutate({ runId: item.run_id, decision: 'approved' })
  }

  function handleReject() {
    approveRun.mutate({ runId: item.run_id, decision: 'denied' })
  }

  function handleDismiss() {
    setDismissed(true)
    onDismiss(item.run_id)
  }

  return (
    <div className={styles.item}>
      <div className={`${styles.accent} ${accentClassMap[item.type] ?? ''}`} />
      <div className={styles.itemBody}>
        <div className={styles.itemHeader}>
          <TypeBadge type={item.type} />
          <Link to={`/runs/${item.run_id}`} className={styles.policyLink}>
            {item.policy_name}
          </Link>
          {item.expires_at && <CountdownDisplay expiresAt={item.expires_at} />}
          <div className={styles.actions}>
            {item.type === 'approval' && (
              <>
                <button
                  className={`${styles.actionButton} ${styles.actionApprove}`}
                  onClick={handleApprove}
                  disabled={approveRun.isPending}
                >
                  Approve
                </button>
                <button
                  className={`${styles.actionButton} ${styles.actionReject}`}
                  onClick={handleReject}
                  disabled={approveRun.isPending}
                >
                  Reject
                </button>
              </>
            )}
            {item.type === 'feedback' && (
              <button
                className={`${styles.actionButton} ${styles.actionFeedback}`}
                onClick={() => navigate(`/runs/${item.run_id}`)}
              >
                Respond
              </button>
            )}
            {/* No inline approve/reject for a tool ask. Unlike an ADR-008
                approval, the operator is answering a question a server asked,
                and the question — plus its schema, its deadline source, and
                any prior attempt — only exists on the run detail card. A
                one-click Approve here would be consent given without having
                read what was asked. */}
            {item.type === 'tool_input' && (
              <button
                className={`${styles.actionButton} ${styles.actionFeedback}`}
                onClick={() => navigate(`/runs/${item.run_id}`)}
              >
                {item.elicitation_kind === 'permission' ? 'Review' : 'Respond'}
              </button>
            )}
            {item.type === 'failure' && (
              <>
                <button
                  className={`${styles.actionButton} ${styles.actionView}`}
                  onClick={() => navigate(`/runs/${item.run_id}`)}
                >
                  View Run
                </button>
                <button
                  className={styles.dismissButton}
                  onClick={handleDismiss}
                  aria-label="Dismiss"
                >
                  ×
                </button>
              </>
            )}
          </div>
        </div>
        <div className={styles.detail}>
          {item.type === 'approval' && (
            <>Tool <code className={styles.toolName}>{item.tool_name}</code> requires approval</>
          )}
          {item.type === 'feedback' && item.message.slice(0, 120)}
          {/* Server-controlled text (§6.1). React escapes it; it is never run
              through a markdown renderer here or anywhere else. */}
          {item.type === 'tool_input' && (
            <>
              <code className={styles.toolName}>{item.tool_name}</code>{' '}
              {item.message ? item.message.slice(0, 120) : 'is asking for a response'}
            </>
          )}
          {item.type === 'failure' && item.message.slice(0, 120)}
        </div>
        {approveRun.isError && item.type === 'approval' && (
          <p className={styles.inlineError}>Failed to submit decision — please try again</p>
        )}
      </div>
    </div>
  )
}
