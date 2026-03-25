export type RunStatus = 'complete' | 'running' | 'waiting_for_approval' | 'failed' | 'interrupted' | 'pending';
export type TriggerType = 'webhook' | 'manual' | 'scheduled';

export interface ReasoningStep {
  type: 'thought' | 'tool_call' | 'tool_result';
  text: string;
  detail?: string;
}

export interface ApprovalDef {
  id: string;
  runId: string;
  policyId: string;
  policyName: string;
  folder: string;
  toolName: string;
  proposedInput: Record<string, unknown>;
  agentSummary: string;
  reasoning: ReasoningStep[];
  expiresAt: string;
  startedAt: string;
}

export const STATUS_CONFIG: Record<RunStatus, { label: string }> = {
  complete:             { label: 'Complete' },
  running:              { label: 'Running' },
  waiting_for_approval: { label: 'Awaiting Approval' },
  failed:               { label: 'Failed' },
  interrupted:          { label: 'Interrupted' },
  pending:              { label: 'Pending' },
};
