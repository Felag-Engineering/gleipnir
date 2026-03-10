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
  color: string;
  bg: string;
  border: string;
  pulse?: boolean;
}> = {
  complete:             { label: 'Complete',          color: '#4ade80', bg: 'rgba(74,222,128,0.08)',  border: 'rgba(74,222,128,0.2)' },
  running:              { label: 'Running',           color: '#60a5fa', bg: 'rgba(96,165,250,0.08)',  border: 'rgba(96,165,250,0.2)', pulse: true },
  waiting_for_approval: { label: 'Awaiting Approval', color: '#f59e0b', bg: 'rgba(245,158,11,0.08)',  border: 'rgba(245,158,11,0.2)', pulse: true },
  failed:               { label: 'Failed',            color: '#f87171', bg: 'rgba(248,113,113,0.08)', border: 'rgba(248,113,113,0.2)' },
  interrupted:          { label: 'Interrupted',       color: '#a78bfa', bg: 'rgba(167,139,250,0.08)', border: 'rgba(167,139,250,0.2)' },
};
