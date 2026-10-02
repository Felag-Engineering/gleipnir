// Shared fixtures for fan-out result tests and stories. Shaped exactly as
// Relay returns them: rows sorted by node_id, exit_code omitted on
// did-not-run rows, and duration_ms: 0 for outcomes that never dispatched.
import type { FanOutResult, FanOutRow } from './fanOutResult'

// asToolOutput returns the exact stored form of a tool_result step's output:
// an MCP text-content envelope whose single item's text is the compact JSON.
export function asToolOutput(obj: unknown): string {
  return JSON.stringify([{ type: 'text', text: JSON.stringify(obj) }])
}

function successRow(nodeId: string): FanOutRow {
  return {
    node_id: nodeId,
    outcome: 'success',
    exit_code: 0,
    stdout: 'ok\n',
    stderr: '',
    stdout_truncated: false,
    stderr_truncated: false,
    duration_ms: 842,
  }
}

const ALL_OK_ROWS: FanOutRow[] = Array.from({ length: 24 }, (_, i) =>
  successRow(`node-${String(i + 1).padStart(2, '0')}`),
)

export const ALL_OK_24: FanOutResult = {
  job_id: 'job-all-ok',
  results: ALL_OK_ROWS,
}

const MIXED_ROWS: FanOutRow[] = [
  {
    node_id: 'node-01',
    outcome: 'denied_by_policy',
    stdout: '',
    stderr: 'refused: destructive operation blocked by policy',
    stdout_truncated: false,
    stderr_truncated: false,
    duration_ms: 0,
    refusal_explanation: 'Policy "no-destructive-ops" blocks raw_exec with rm -rf.',
  },
  {
    node_id: 'node-02',
    outcome: 'denied_by_policy',
    stdout: '',
    stderr: 'refused: destructive operation blocked by policy',
    stdout_truncated: false,
    stderr_truncated: false,
    duration_ms: 0,
    refusal_explanation: 'Policy "no-destructive-ops" blocks raw_exec with rm -rf.',
  },
  {
    node_id: 'node-03',
    outcome: 'unreachable',
    stdout: '',
    stderr: '',
    stdout_truncated: false,
    stderr_truncated: false,
    duration_ms: 0,
  },
  {
    node_id: 'node-04',
    outcome: 'failure',
    exit_code: 1,
    stdout: 'starting restart\n',
    stderr: 'systemctl: unit not found\n',
    stdout_truncated: false,
    stderr_truncated: false,
    duration_ms: 1204,
  },
  {
    node_id: 'node-05',
    outcome: 'version_refused',
    stdout: '',
    stderr: '',
    stdout_truncated: false,
    stderr_truncated: false,
    duration_ms: 0,
    refusal_explanation: 'Node agent version 0.7.2 is below the required minimum 0.9.0.',
  },
  ...Array.from({ length: 19 }, (_, i) => successRow(`node-${String(i + 6).padStart(2, '0')}`)),
]

export const MIXED_24: FanOutResult = {
  job_id: 'job-mixed',
  results: MIXED_ROWS,
}

export const SINGLE: FanOutResult = {
  job_id: 'job-single',
  results: [
    {
      node_id: 'node-01',
      outcome: 'success',
      exit_code: 0,
      stdout: Array.from({ length: 50 }, (_, i) => `log line ${i}`).join('\n'),
      stderr: '',
      stdout_truncated: true,
      stderr_truncated: false,
      duration_ms: 3120,
    },
  ],
}

export const EMPTY: FanOutResult = {
  job_id: 'job-empty',
  results: [],
}

// APPROVED_RETRY is what Relay returns on the answered retry of an in-band
// approval: the Job the approval released, plus the decision block. Key order
// matches Relay's JSON encoding (decision is marshalled after results).
export const APPROVED_RETRY: FanOutResult = {
  job_id: 'job-approved',
  results: [
    successRow('node-1123aea7732badca90d02d21bd1aac96'),
    successRow('node-814e5b14eca5206060abdb164672609f'),
  ],
  decision: {
    request_id: '172e493bee384ac7eef1aae480536ee9',
    state: 'approved',
    progress: '1 of 1',
    channel: 'in-band',
    on_behalf_of: 'dana',
    next_step:
      'the call was re-entered through the full dispatch pipeline once in this response, and the Job shown ran. Do not re-issue it. the answer was recorded (progress shown when the rule needs more).',
  },
}

// DENIED_RETRY is what Relay returns when the approver rejects: the decision,
// no job_id (nothing was dispatched), and an empty results array — the exact
// RunOrPlanOutput answerThenDispatch renders on its elicitedDenied branch.
export const DENIED_RETRY: FanOutResult = {
  results: [],
  decision: {
    request_id: '01efcc8e22e45196e7f08119d7e9eeb5',
    state: 'denied',
    progress: '1 of 1',
    channel: 'in-band',
    on_behalf_of: 'dana',
    next_step:
      'a denial is terminal. Nothing ran; do not retry. the answer was recorded (progress shown when the rule needs more).',
  },
}

// ANSWER_REFUSED is Relay's refusal of an answer it would not take as a
// decision (here: a request already decided before the answer arrived), with
// nothing dispatched. Text from tools_approval.go's refusalOutput.
export const ANSWER_REFUSED: FanOutResult = {
  results: [],
  answer_refused: {
    request_id: '01efcc8e22e45196e7f08119d7e9eeb5',
    reason: 'approval request is not pending (already decided or expired)',
    state: 'denied',
    expires_at: '2026-10-01T13:48:18Z',
    next_step: 'this request is no longer open (already decided, or expired); it cannot be decided again.',
  },
}

// The demo fleet's nine-Node roster: node ids as Relay derives them, with the
// hostnames each Node reports in list_nodes' facts.hostname.
export const DEMO_ROSTER: [string, string][] = [
  ['node-eecbf23118c110335d9bddae81acd086', 'dev-node-1'],
  ['node-ced0ade997288f48f027586edc56503a', 'dev-node-2'],
  ['node-14ddeeecb53328ddab9ef01abf67ccb4', 'dev-node-3'],
  ['node-814e5b14eca5206060abdb164672609f', 'dev-node-4'],
  ['node-1123aea7732badca90d02d21bd1aac96', 'dev-node-5'],
  ['node-e9b3da9bc561ead6f9bdfd1e88a8b977', 'dev-node-6'],
  ['node-875ce40155e0f76608693dd55c551a1d', 'dev-node-7'],
  ['node-09671d9abbfdc18526b3bac7e42291d7', 'dev-node-8'],
  ['node-360f6bdabd5b0fbf029d6b75a439cf83', 'dev-node-9'],
]

export const DEMO_HOSTNAMES: ReadonlyMap<string, string> = new Map(DEMO_ROSTER)

// listNodesOutput builds a list_nodes result (Relay's ListNodesOutput) for the
// given roster entries, shaped as Relay's NodeItem.
export function listNodesOutput(roster: [string, string][]): unknown {
  return {
    nodes: roster.map(([nodeId, hostname]) => ({
      node_id: nodeId,
      node_labels: { role: 'web', env: 'prod' },
      relay_labels: {},
      facts: { hostname, os: 'Alpine Linux v3.20', kernel: '6.8.0', arch: 'x86_64', ip_addresses: ['172.28.0.14'] },
      last_seen: '2026-10-01T12:48:16Z',
      policy_fingerprint: 'abc',
      template_set_fingerprint: '',
      revoked: false,
      outdated: false,
      connected: true,
      state_labels: ['state:online'],
    })),
  }
}

// DEMO_MIXED is the wide demo variant (node:env=prod): two web Nodes restart,
// the db Node has no systemctl, and the bastion's Policy refuses.
export const DEMO_MIXED: FanOutResult = {
  job_id: '5d1c0e8f3a7b49e2a6c4f0b18e9d7a21',
  results: [
    {
      node_id: 'node-09671d9abbfdc18526b3bac7e42291d7',
      outcome: 'denied_by_policy',
      stdout: '',
      stderr: "deny-by-default: no allow rule for operation 'service.restart'",
      stdout_truncated: false,
      stderr_truncated: false,
      duration_ms: 3,
      refusal_explanation: 'Refused by the Node, not by this control plane — a refusal, not a failure: nothing executed.',
    },
    { ...successRow('node-1123aea7732badca90d02d21bd1aac96'), stdout: '' },
    { ...successRow('node-814e5b14eca5206060abdb164672609f'), stdout: '' },
    {
      node_id: 'node-875ce40155e0f76608693dd55c551a1d',
      outcome: 'failure',
      stdout: '',
      stderr: 'spawn systemctl: No such file or directory (os error 2)',
      stdout_truncated: false,
      stderr_truncated: false,
      duration_ms: 4,
    },
  ],
  decision: APPROVED_RETRY.decision,
}
