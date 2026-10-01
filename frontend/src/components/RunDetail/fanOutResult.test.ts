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

describe('parseFanOutResult — answered approval retry', () => {
  const decision = {
    request_id: 'req-1',
    state: 'approved',
    progress: '1 of 1',
    channel: 'in-band',
    on_behalf_of: 'dana',
    next_step: 'the Job shown ran. Do not re-issue it.',
  }

  it('accepts {job_id, results, decision} and keeps the decision', () => {
    const shape = { ...GET_JOB_SHAPE, decision }
    expect(parseFanOutResult(JSON.stringify(shape))).toEqual(shape)
  })

  it('accepts a decision whose channel and on_behalf_of are empty strings', () => {
    const shape = { ...GET_JOB_SHAPE, decision: { ...decision, channel: '', on_behalf_of: '' } }
    expect(parseFanOutResult(shape)).toEqual(shape)
  })

  it('omits decision from the parsed value when Relay sent none', () => {
    expect(parseFanOutResult(GET_JOB_SHAPE)).not.toHaveProperty('decision')
  })

  it('rejects a decision with an unknown key', () => {
    expect(parseFanOutResult({ ...GET_JOB_SHAPE, decision: { ...decision, extra: 'x' } })).toBeNull()
  })

  it('rejects a decision missing a field', () => {
    const { next_step: _omitted, ...partial } = decision
    expect(parseFanOutResult({ ...GET_JOB_SHAPE, decision: partial })).toBeNull()
  })

  it('rejects a decision with a non-string field', () => {
    expect(parseFanOutResult({ ...GET_JOB_SHAPE, decision: { ...decision, progress: 1 } })).toBeNull()
  })

  it('rejects a decision that is not an object', () => {
    expect(parseFanOutResult({ ...GET_JOB_SHAPE, decision: 'approved' })).toBeNull()
  })

})

// Shapes from Relay's answerThenDispatch (tools_execution.go): every branch
// that dispatches nothing renders {results: [], decision | answer_refused}
// with job_id omitted.
describe('parseFanOutResult — answered retry that dispatched nothing', () => {
  const denied = {
    request_id: 'req-1',
    state: 'denied',
    progress: '1 of 1',
    channel: 'in-band',
    on_behalf_of: 'dana',
    next_step: 'a denial is terminal. Nothing ran; do not retry. the answer was recorded (progress shown when the rule needs more).',
  }
  const refusal = {
    request_id: 'req-1',
    reason: 'approval request is not pending (already decided or expired)',
    state: 'denied',
    expires_at: '2026-10-01T13:48:18Z',
    next_step: 'this request is no longer open (already decided, or expired); it cannot be decided again.',
  }

  it.each([
    ['a denial: {results: [], decision}', { results: [], decision: denied }],
    ['a denial as the JSON text Relay sends', JSON.stringify({ decision: denied, results: [] })],
    ['an expired request with nothing dispatched', { results: [], decision: { ...denied, state: 'expired', channel: '', on_behalf_of: '' } }],
    ['a refused answer: {results: [], answer_refused}', { results: [], answer_refused: refusal }],
    ['a refused answer with only the two required fields', { results: [], answer_refused: { reason: 'r', next_step: 'n' } }],
    ['a refused answer naming a Job released earlier', { results: [], answer_refused: { ...refusal, state: 'approved', job_id: 'job-9' } }],
  ])('accepts %s', (_label, shape) => {
    const parsed = parseFanOutResult(shape)
    expect(parsed).not.toBeNull()
    expect(parsed).not.toHaveProperty('job_id')
    expect(parsed!.results).toEqual([])
  })

  it('keeps the decision verbatim', () => {
    expect(parseFanOutResult({ results: [], decision: denied })).toEqual({ results: [], decision: denied })
  })

  it('keeps the refusal verbatim', () => {
    expect(parseFanOutResult({ results: [], answer_refused: refusal })).toEqual({ results: [], answer_refused: refusal })
  })

  it.each([
    ['neither decision nor answer_refused', { results: [] }],
    ['both decision and answer_refused', { results: [], decision: denied, answer_refused: refusal }],
    ['rows without a job_id', { results: GET_JOB_SHAPE.results, decision: denied }],
    ['a re-parked retry (pending_approval beside the decision)', { results: [], decision: denied, pending_approval: { status: 'pending_approval' } }],
    ['an answer_refused with an unknown key', { results: [], answer_refused: { ...refusal, extra: 'x' } }],
    ['an answer_refused missing reason', { results: [], answer_refused: { next_step: 'n' } }],
    ['an answer_refused missing next_step', { results: [], answer_refused: { reason: 'r' } }],
    ['an answer_refused with a non-string field', { results: [], answer_refused: { ...refusal, state: 2 } }],
    ['answer_refused beside a job_id', { ...GET_JOB_SHAPE, answer_refused: refusal }],
    ['a decision missing a field', { results: [], decision: { ...denied, progress: undefined } }],
  ])('rejects %s', (_label, shape) => {
    expect(parseFanOutResult(JSON.parse(JSON.stringify(shape)))).toBeNull()
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
