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

// runStatusLabel is the operator-facing label for a run's status.
//
// A run paused on a tool-initiated *permission* ask (ADR-055 spec §6.1 — an
// MCP server asking a human to approve before it proceeds) sits in the
// backend's `waiting_for_feedback` state, because that is the state a run
// waiting on a human for any tool-initiated answer is in. To the person
// looking at the badge, though, it is an approval, and calling it "feedback"
// tells an approver it is not theirs to answer. Only the label changes: the
// run state itself is the canonical state machine and stays as it is.
export function runStatusLabel(status: RunStatus, awaitingPermission = false): string {
  if (status === 'waiting_for_feedback' && awaitingPermission) {
    return STATUS_CONFIG.waiting_for_approval.label
  }
  return STATUS_CONFIG[status].label
}
