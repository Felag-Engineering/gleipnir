export interface AssignedTool {
  toolId: string;
  serverId: string;
  serverName: string;
  name: string;
  description: string;
  approvalRequired: boolean;
  approvalTimeout: string; // Go duration string (e.g. "30m"), empty string if unset
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

export type TriggerType = 'webhook' | 'manual' | 'scheduled' | 'poll' | 'cron';

export type WebhookAuthMode = 'hmac' | 'bearer' | 'none';

export interface WebhookTriggerState {
  type: 'webhook';
  auth: WebhookAuthMode;
}

export interface ManualTriggerState {
  type: 'manual';
}

export interface ScheduledTriggerState {
  type: 'scheduled';
  fireAt: string[]; // ISO-8601 / RFC3339 timestamps
}

export interface PollCheckState {
  tool: string;        // dot-notation server.tool
  input: string;       // JSON string for the input map (edited as text)
  path: string;        // JSONPath expression (e.g. "$.status")
  comparator: 'equals' | 'not_equals' | 'greater_than' | 'less_than' | 'contains';
  value: string;       // comparator operand (always string in form, coerced to number/bool on YAML serialization)
}

export interface PollTriggerState {
  type: 'poll';
  interval: string;        // e.g. "5m"
  match: 'all' | 'any';
  checks: PollCheckState[];
}

export interface CronTriggerState {
  type: 'cron';
  cronExpr: string; // 5-field POSIX cron expression
}

export type TriggerFormState =
  | WebhookTriggerState
  | ManualTriggerState
  | ScheduledTriggerState
  | PollTriggerState
  | CronTriggerState;

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
  queueDepth: number; // 0 means unset/use backend default; only emitted when mode is 'queue' and > 0
}

export interface ModelFormState {
  provider: string;
  model: string;
}

// FormIssue is defined in validateFormState.ts (single source of truth).
// It is re-exported here so section components only need to import from './types'.
// SectionIssues is the array form: a flat list of issues pre-filtered to a
// specific form section.
import type { FormIssue } from '@/components/AgentEditor/validateFormState';
export type { FormIssue };
export type SectionIssues = FormIssue[];
