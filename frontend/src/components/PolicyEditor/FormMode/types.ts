import type { CapabilityRole } from '@/components/RoleBadge';

export interface AssignedTool {
  toolId: string;
  serverId: string;
  serverName: string;
  name: string;
  description: string;
  role: CapabilityRole;
  approvalRequired: boolean; // only meaningful for actuators
}

export interface CapabilitiesFormState {
  tools: AssignedTool[];
}

export interface IdentityFormState {
  name: string;
  description: string;
  folder: string;
}

export type TriggerType = 'webhook' | 'cron' | 'poll' | 'manual';

export interface WebhookTriggerState {
  type: 'webhook';
}

export interface CronTriggerState {
  type: 'cron';
  schedule: string;
}

export interface PollTriggerState {
  type: 'poll';
  interval: string;
  request: {
    url: string;
    method: 'GET' | 'POST';
    headers: string; // textarea: one "Key: Value" per line
    body?: string;
  };
  filter: string;
}

export interface ManualTriggerState {
  type: 'manual';
}

export type TriggerFormState =
  | WebhookTriggerState
  | CronTriggerState
  | PollTriggerState
  | ManualTriggerState;

export interface TaskInstructionsFormState {
  task: string;
}

export interface RunLimitsFormState {
  max_tokens_per_run: number;
  max_tool_calls_per_run: number;
}

export type ConcurrencyValue = 'skip' | 'queue' | 'parallel' | 'replace';

export interface ConcurrencyFormState {
  concurrency: ConcurrencyValue;
}

export type ModelValue = 'claude-opus-4-6' | 'claude-sonnet-4-6' | 'claude-haiku-4-5-20251001';

export interface ModelFormState {
  model: ModelValue;
}
