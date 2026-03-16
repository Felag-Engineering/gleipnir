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

export type TriggerType = 'webhook' | 'manual' | 'scheduled';

export interface WebhookTriggerState {
  type: 'webhook';
}

export interface ManualTriggerState {
  type: 'manual';
}

export interface ScheduledTriggerState {
  type: 'scheduled';
  fireAt: string[]; // ISO-8601 / RFC3339 timestamps
}

export type TriggerFormState =
  | WebhookTriggerState
  | ManualTriggerState
  | ScheduledTriggerState;

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
