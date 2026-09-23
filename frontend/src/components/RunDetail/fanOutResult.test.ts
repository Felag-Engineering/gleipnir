import { describe, it, expect } from 'vitest'
import { groupByOutcome, outcomeTone, parseFanOutResult } from './fanOutResult'
import type { FanOutRow } from './fanOutResult'

const GET_JOB_SHAPE = {
  job_id: 'job-1',
  results: [
    {
      node_id: 'node-01',
      outcome: 'success',
      exit_code: 0,
      stdout: 'ok\n',
      stderr: '',
      stdout_truncated: false,
      stderr_truncated: false,
      duration_ms: 500,
    },
  ],
}

describe('parseFanOutResult — accepts', () => {
  it('a get_job-shaped object as a JSON string (what parseToolOutput returns for a real Relay call)', () => {
    expect(parseFanOutResult(JSON.stringify(GET_JOB_SHAPE))).toEqual(GET_JOB_SHAPE)
  })

  it('the same thing already parsed into an object', () => {
    expect(parseFanOutResult(GET_JOB_SHAPE)).toEqual(GET_JOB_SHAPE)
  })

  it('{job_id, results: []}', () => {
    const shape = { job_id: 'job-empty', results: [] }
    expect(parseFanOutResult(shape)).toEqual(shape)
  })

  it('a row without exit_code', () => {
    const shape = {
      job_id: 'job-1',
      results: [
        {
          node_id: 'node-01',
          outcome: 'unreachable',
          stdout: '',
          stderr: '',
          stdout_truncated: false,
          stderr_truncated: false,
          duration_ms: 0,
        },
      ],
    }
    expect(parseFanOutResult(shape)).toEqual(shape)
  })

  it('a row with refusal_explanation', () => {
    const shape = {
      job_id: 'job-1',
      results: [
        {
          node_id: 'node-01',
          outcome: 'denied_by_policy',
          stdout: '',
          stderr: 'refused',
          stdout_truncated: false,
          stderr_truncated: false,
          duration_ms: 0,
          refusal_explanation: 'Policy blocks this.',
        },
      ],
    }
    expect(parseFanOutResult(shape)).toEqual(shape)
  })

  it('an unknown outcome string such as "quarantined"', () => {
    const shape = {
      job_id: 'job-1',
      results: [
        {
          node_id: 'node-01',
          outcome: 'quarantined',
          stdout: '',
          stderr: '',
          stdout_truncated: false,
          stderr_truncated: false,
          duration_ms: 0,
        },
      ],
    }
    expect(parseFanOutResult(shape)).toEqual(shape)
  })
})

describe('parseFanOutResult — rejects', () => {
  it('non-JSON text', () => {
    expect(parseFanOutResult('INFO ready')).toBeNull()
  })

  it('a JSON array', () => {
    expect(parseFanOutResult(JSON.stringify([{ job_id: 'x', results: [] }]))).toBeNull()
  })

  it('missing job_id', () => {
    expect(parseFanOutResult({ results: [] })).toBeNull()
  })

  it('empty job_id', () => {
    expect(parseFanOutResult({ job_id: '', results: [] })).toBeNull()
  })

  it('a plan response {"results":[],"plan":{...}}', () => {
    expect(parseFanOutResult({ results: [], plan: { steps: [] } })).toBeNull()
  })

  it('a parked response {"results":[],"pending_approval":{...}}', () => {
    expect(parseFanOutResult({ results: [], pending_approval: { id: 'approval-1' } })).toBeNull()
  })

  it('an extra top-level key', () => {
    expect(parseFanOutResult({ job_id: 'job-1', results: [], extra: true })).toBeNull()
  })

  it('results that is not an array', () => {
    expect(parseFanOutResult({ job_id: 'job-1', results: {} })).toBeNull()
  })

  it('a row with an extra key', () => {
    const row = {
      node_id: 'node-01',
      outcome: 'success',
      stdout: '',
      stderr: '',
      stdout_truncated: false,
      stderr_truncated: false,
      duration_ms: 0,
      new_field: 'x',
    }
    expect(parseFanOutResult({ job_id: 'job-1', results: [row] })).toBeNull()
  })

  it('a row whose exit_code is not an integer', () => {
    const row = {
      node_id: 'node-01',
      outcome: 'success',
      exit_code: 1.5,
      stdout: '',
      stderr: '',
      stdout_truncated: false,
      stderr_truncated: false,
      duration_ms: 0,
    }
    expect(parseFanOutResult({ job_id: 'job-1', results: [row] })).toBeNull()
  })

  it('a row whose exit_code is a string', () => {
    const row = {
      node_id: 'node-01',
      outcome: 'success',
      exit_code: '0',
      stdout: '',
      stderr: '',
      stdout_truncated: false,
      stderr_truncated: false,
      duration_ms: 0,
    }
    expect(parseFanOutResult({ job_id: 'job-1', results: [row] })).toBeNull()
  })

  it('a row whose duration_ms is a string', () => {
    const row = {
      node_id: 'node-01',
      outcome: 'success',
      stdout: '',
      stderr: '',
      stdout_truncated: false,
      stderr_truncated: false,
      duration_ms: '500',
    }
    expect(parseFanOutResult({ job_id: 'job-1', results: [row] })).toBeNull()
  })

  // Each required row field, missing one at a time.
  const FULL_ROW: FanOutRow = {
    node_id: 'node-01',
    outcome: 'success',
    stdout: 'ok',
    stderr: '',
    stdout_truncated: false,
    stderr_truncated: false,
    duration_ms: 500,
  }
  const REQUIRED_FIELDS = [
    'node_id',
    'outcome',
    'stdout',
    'stderr',
    'stdout_truncated',
    'stderr_truncated',
    'duration_ms',
  ] as const

  it.each(REQUIRED_FIELDS)('a row missing required field %s', (field) => {
    const row: Record<string, unknown> = { ...FULL_ROW }
    delete row[field]
    expect(parseFanOutResult({ job_id: 'job-1', results: [row] })).toBeNull()
  })
})

describe('groupByOutcome', () => {
  it('orders exceptions before success, following OUTCOME_ORDER', () => {
    const rows: FanOutRow[] = [
      { node_id: 'a', outcome: 'success', stdout: '', stderr: '', stdout_truncated: false, stderr_truncated: false, duration_ms: 0 },
      { node_id: 'b', outcome: 'unreachable', stdout: '', stderr: '', stdout_truncated: false, stderr_truncated: false, duration_ms: 0 },
      { node_id: 'c', outcome: 'denied_by_policy', stdout: '', stderr: '', stdout_truncated: false, stderr_truncated: false, duration_ms: 0 },
      { node_id: 'd', outcome: 'failure', stdout: '', stderr: '', stdout_truncated: false, stderr_truncated: false, duration_ms: 0 },
    ]
    const groups = groupByOutcome(rows)
    expect(groups.map((g) => g.outcome)).toEqual(['denied_by_policy', 'failure', 'unreachable', 'success'])
  })

  it('sorts an unknown outcome before success, in first-seen order', () => {
    const rows: FanOutRow[] = [
      { node_id: 'a', outcome: 'success', stdout: '', stderr: '', stdout_truncated: false, stderr_truncated: false, duration_ms: 0 },
      { node_id: 'b', outcome: 'quarantined', stdout: '', stderr: '', stdout_truncated: false, stderr_truncated: false, duration_ms: 0 },
    ]
    const groups = groupByOutcome(rows)
    expect(groups.map((g) => g.outcome)).toEqual(['quarantined', 'success'])
  })

  it('omits empty groups', () => {
    const rows: FanOutRow[] = [
      { node_id: 'a', outcome: 'success', stdout: '', stderr: '', stdout_truncated: false, stderr_truncated: false, duration_ms: 0 },
    ]
    expect(groupByOutcome(rows)).toEqual([{ outcome: 'success', rows: [rows[0]] }])
  })

  it('keeps node_id order stable within a group', () => {
    const rows: FanOutRow[] = [
      { node_id: 'node-03', outcome: 'success', stdout: '', stderr: '', stdout_truncated: false, stderr_truncated: false, duration_ms: 0 },
      { node_id: 'node-01', outcome: 'success', stdout: '', stderr: '', stdout_truncated: false, stderr_truncated: false, duration_ms: 0 },
    ]
    expect(groupByOutcome(rows)[0].rows.map((r) => r.node_id)).toEqual(['node-03', 'node-01'])
  })
})

describe('outcomeTone', () => {
  it.each([
    ['success', 'ok'],
    ['denied_by_policy', 'policy'],
    ['failure', 'failed'],
    ['timeout', 'failed'],
    ['unreachable', 'notRun'],
    ['version_refused', 'notRun'],
    ['unsupported', 'notRun'],
    ['cancelled', 'notRun'],
    ['unspecified', 'unknown'],
    ['quarantined', 'unknown'],
  ] as const)('%s → %s', (outcome, tone) => {
    expect(outcomeTone(outcome)).toBe(tone)
  })
})
