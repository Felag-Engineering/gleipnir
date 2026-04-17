import { Button } from '@/components/Button/Button'
import { useApproveRun } from '@/hooks/mutations/runs'
import { useCurrentUser } from '@/hooks/queries/users'
import { useCountdown } from '@/hooks/useCountdown'
import styles from './ApprovalActions.module.css'

interface Props {
  runId: string
  runStatus: string
  approvalExpiresAt?: string | null
}

export function ApprovalActions({ runId, runStatus, approvalExpiresAt }: Props) {
  const { data: user } = useCurrentUser()
  const approveRun = useApproveRun()

  // useCountdown must be called unconditionally before any early returns (Rules of Hooks).
  const countdown = useCountdown(approvalExpiresAt ?? undefined)

  if (runStatus !== 'waiting_for_approval') {
    return null
  }

  const canApprove = user?.roles.some((r) => r === 'approver' || r === 'admin')
  if (!canApprove) {
    return null
  }

  return (
    <div>
      <div className={styles.actions}>
        <Button
          variant="primary"
          size="small"
          disabled={approveRun.isPending}
          onClick={() => approveRun.mutate({ runId, decision: 'approved' })}
        >
          Approve
        </Button>
        <Button
          variant="danger"
          size="small"
          disabled={approveRun.isPending}
          onClick={() => approveRun.mutate({ runId, decision: 'denied' })}
        >
          Deny
        </Button>
        {countdown && (
          <span className={`${styles.countdown} ${countdown.urgent ? styles.countdownUrgent : ''}`}>
            {countdown.str}
          </span>
        )}
      </div>
      {approveRun.isError && (
        <p className={styles.error}>
          {approveRun.error instanceof Error
            ? approveRun.error.message
            : 'Failed to submit decision'}
        </p>
      )}
    </div>
  )
}
