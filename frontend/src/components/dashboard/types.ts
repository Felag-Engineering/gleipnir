import type { RunStatus, TriggerType } from '@/constants/status'
export type { RunStatus, TriggerType }

export const STATUS_CONFIG: Record<RunStatus, { label: string }> = {
  complete:             { label: 'Complete' },
  running:              { label: 'Running' },
  waiting_for_approval: { label: 'Awaiting Approval' },
  waiting_for_feedback: { label: 'Awaiting Feedback' },
  failed:               { label: 'Failed' },
  interrupted:          { label: 'Interrupted' },
  pending:              { label: 'Pending' },
};
