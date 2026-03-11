export interface IdentityFormState {
  name: string;
  description: string;
  folder: string;
}

export type TriggerType = 'webhook' | 'cron' | 'poll';

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

export type TriggerFormState =
  | WebhookTriggerState
  | CronTriggerState
  | PollTriggerState;
