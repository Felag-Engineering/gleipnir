export interface AssignedTool {
  toolId: string;
  serverId: string;
  serverName: string;
  name: string;
  description: string;
  approvalRequired: boolean;
}

export interface FeedbackFormState {
  enabled: boolean;
  timeout: string;   // Go duration string (e.g. "30m"), empty string if not set
  onTimeout: string; // "fail" (currently the only valid value)
}

export interface CapabilitiesFormState {
  tools: AssignedTool[];
  feedback: FeedbackFormState;
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

export interface ModelFormState {
  provider: string;
  model: string;
}
