import type { RunStatus, TriggerType, ApprovalDef } from './types';

interface Run {
  id: string;
  status: RunStatus;
  startedAt: string;
  duration: number | null;
  tokenCost: number;
  toolCalls: number;
  summary: string | null;
}

interface Policy {
  id: string;
  name: string;
  triggerType: TriggerType;
  latestRun: Run;
  history: Run[];
}

interface Folder {
  id: string;
  name: string;
  policies: Policy[];
}

export const SAMPLE_FOLDERS: Folder[] = [
  {
    id: 'f1', name: 'Vikunja',
    policies: [
      {
        id: 'p1', name: 'vikunja-triage', triggerType: 'webhook',
        latestRun: { id: 'r101', status: 'complete', startedAt: '2026-03-07T14:32:11Z', duration: 47, tokenCost: 8420, toolCalls: 12, summary: 'Triaged task #1041 — P2, related Grafana alert resolved.' },
        history: [
          { id: 'r100', status: 'complete',    startedAt: '2026-03-07T13:10:22Z', duration: 62, tokenCost: 11200, toolCalls: 18, summary: 'Triaged task #1039 — P1, linked to active Grafana alert.' },
          { id: 'r099', status: 'failed',      startedAt: '2026-03-07T11:55:44Z', duration:  3, tokenCost:   420, toolCalls:  1, summary: 'Tool call limit exceeded before completion.' },
          { id: 'r098', status: 'complete',    startedAt: '2026-03-07T09:21:08Z', duration: 38, tokenCost:  7100, toolCalls: 11, summary: 'Triaged task #1037 — P3, no related alerts.' },
          { id: 'r097', status: 'complete',    startedAt: '2026-03-06T16:44:55Z', duration: 55, tokenCost:  9300, toolCalls: 14, summary: 'Triaged task #1035 — P2, pod restarts on api-gateway.' },
          { id: 'r096', status: 'interrupted', startedAt: '2026-03-06T14:12:00Z', duration:  5, tokenCost:   890, toolCalls:  2, summary: 'Interrupted: Gleipnir restarted during execution.' },
        ],
      },
      {
        id: 'p2', name: 'vikunja-daily-digest', triggerType: 'scheduled',
        latestRun: { id: 'r201', status: 'running', startedAt: '2026-03-07T08:00:00Z', duration: null, tokenCost: 1850, toolCalls: 4, summary: null },
        history: [
          { id: 'r200', status: 'complete', startedAt: '2026-03-06T08:00:00Z', duration: 84, tokenCost: 10200, toolCalls: 22, summary: 'Digest posted — 7 overdue tasks across 3 projects.' },
          { id: 'r199', status: 'complete', startedAt: '2026-03-05T08:00:00Z', duration: 71, tokenCost:  8900, toolCalls: 19, summary: 'Digest posted — 4 overdue tasks.' },
        ],
      },
      {
        id: 'p3', name: 'vikunja-close-resolved', triggerType: 'webhook',
        latestRun: { id: 'r301', status: 'waiting_for_approval', startedAt: '2026-03-07T14:29:05Z', duration: null, tokenCost: 3210, toolCalls: 7, summary: 'Wants to close task #1040 — all criteria met.' },
        history: [
          { id: 'r300', status: 'complete', startedAt: '2026-03-07T12:00:00Z', duration: 29, tokenCost: 4100, toolCalls: 8, summary: 'Closed task #1038 after approval.' },
          { id: 'r299', status: 'complete', startedAt: '2026-03-06T18:30:00Z', duration: 33, tokenCost: 4800, toolCalls: 9, summary: 'Closed task #1036 after approval.' },
        ],
      },
    ],
  },
  {
    id: 'f2', name: 'Grafana',
    policies: [
      {
        id: 'p4', name: 'grafana-alert-responder', triggerType: 'webhook',
        latestRun: { id: 'r401', status: 'complete', startedAt: '2026-03-07T12:44:00Z', duration: 38, tokenCost: 6100, toolCalls: 9, summary: 'Incident task #1038 created for memory-pressure alert on worker-03.' },
        history: [
          { id: 'r400', status: 'complete', startedAt: '2026-03-07T10:12:00Z', duration: 41, tokenCost: 6400, toolCalls: 10, summary: 'Incident task created for CPU spike on api-gateway.' },
          { id: 'r399', status: 'failed',   startedAt: '2026-03-07T06:55:00Z', duration:  4, tokenCost:   510, toolCalls:  2, summary: 'Grafana MCP server unreachable — connection refused.' },
        ],
      },
    ],
  },
  {
    id: 'f3', name: 'Infrastructure',
    policies: [
      {
        id: 'p5', name: 'kubectl-pod-watcher', triggerType: 'webhook',
        latestRun: { id: 'r502', status: 'waiting_for_approval', startedAt: '2026-03-07T14:38:00Z', duration: null, tokenCost: 4100, toolCalls: 6, summary: 'Wants to create P1 incident task — CrashLoopBackOff on worker-02.' },
        history: [
          { id: 'r501', status: 'complete', startedAt: '2026-03-07T14:15:00Z', duration: 22, tokenCost: 3200, toolCalls: 6, summary: 'No CrashLoopBackOff pods detected. All namespaces healthy.' },
          { id: 'r500', status: 'complete', startedAt: '2026-03-07T14:00:00Z', duration: 20, tokenCost: 3100, toolCalls: 5, summary: 'No issues detected.' },
        ],
      },
    ],
  },
];

export const SAMPLE_APPROVALS: ApprovalDef[] = [
  {
    id: 'ap1', runId: 'r301', policyId: 'p3',
    policyName: 'vikunja-close-resolved', folder: 'Vikunja',
    toolName: 'vikunja.task_close',
    proposedInput: { task_id: 1040, reason: 'All acceptance criteria met. No open blockers detected.' },
    agentSummary: 'Wants to close task #1040 — all acceptance criteria met, no open blockers.',
    reasoning: [
      { type: 'thought',     text: 'The trigger payload indicates task #1040 has had its last sub-task checked off. I should verify the task state before closing.' },
      { type: 'tool_call',   text: 'vikunja.task_get',  detail: '{ "task_id": 1040 }' },
      { type: 'tool_result', text: 'Task #1040: \'Deploy auth service to prod\'. Status: open. Sub-tasks: 4/4 complete. No blockers. Due: 2026-03-07.' },
      { type: 'thought',     text: 'All four sub-tasks complete, due date today, no blockers. Criteria met for closure. Actuator requires approval — requesting before proceeding.' },
    ],
    expiresAt:  new Date(Date.now() + 58 * 60 * 1000).toISOString(),
    startedAt:  new Date(Date.now() -  3 * 60 * 1000).toISOString(),
  },
  {
    id: 'ap2', runId: 'r502', policyId: 'p5',
    policyName: 'kubectl-pod-watcher', folder: 'Infrastructure',
    toolName: 'vikunja.task_create',
    proposedInput: {
      title: 'INC: CrashLoopBackOff — worker-02/log-shipper',
      project: 'Incidents', priority: 1,
      description: 'Pod log-shipper in namespace worker-02 has been in CrashLoopBackOff for 12 minutes. Last exit code: 1.',
    },
    agentSummary: 'Wants to create a P1 incident task for a CrashLoopBackOff pod on worker-02.',
    reasoning: [
      { type: 'thought',     text: 'Polling detected CrashLoopBackOff on worker-02/log-shipper. I need to assess severity before creating an incident task.' },
      { type: 'tool_call',   text: 'kubectl.get_pods',   detail: '{ "namespace": "worker-02" }' },
      { type: 'tool_result', text: 'NAME: log-shipper | STATUS: CrashLoopBackOff | RESTARTS: 8 | AGE: 12m' },
      { type: 'tool_call',   text: 'kubectl.get_events', detail: '{ "namespace": "worker-02", "pod": "log-shipper" }' },
      { type: 'tool_result', text: 'Error: failed to connect to log aggregator at 10.0.1.44:5044 — connection refused. Exit code 1.' },
      { type: 'thought',     text: '8 restarts over 12 minutes, connection refused to log aggregator. Log aggregator likely down. P1 incident warranted. Requesting approval.' },
    ],
    expiresAt:  new Date(Date.now() + 18 * 60 * 1000).toISOString(),
    startedAt:  new Date(Date.now() -  7 * 60 * 1000).toISOString(),
  },
];
