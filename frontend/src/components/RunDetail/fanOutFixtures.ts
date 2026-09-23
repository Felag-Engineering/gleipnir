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
