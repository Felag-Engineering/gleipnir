export type RunStatus = 'complete' | 'running' | 'waiting_for_approval' | 'failed' | 'interrupted';
export type TriggerType = 'webhook' | 'cron' | 'poll';

export interface Run {
  id: string;
  status: RunStatus;
  startedAt: string;
  duration: number | null;
  tokenCost: number;
  toolCalls: number;
  summary: string | null;
}

export interface Policy {
  id: string;
  name: string;
  triggerType: TriggerType;
  latestRun: Run;
  history: Run[];
}

export interface Folder {
  id: string;
  name: string;
  policies: Policy[];
}

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

export interface Receipt {
  id: string;
  decision: 'approve' | 'reject';
  note: string;
  settled: boolean;
}

export const STATUS_CONFIG: Record<RunStatus, {
  label: string;
  pulse?: boolean;
}> = {
  complete:             { label: 'Complete' },
  running:              { label: 'Running',           pulse: true },
  waiting_for_approval: { label: 'Awaiting Approval', pulse: true },
  failed:               { label: 'Failed' },
  interrupted:          { label: 'Interrupted' },
};
