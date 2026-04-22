import { describe, it, expect } from 'vitest'
import { validateFormState, parseGoDuration, type FormIssue } from './validateFormState'
import { defaultFormState } from './agentEditorUtils'
import type { FormState } from './agentEditorUtils'

// valid() returns a copy of the default form state pre-populated with all
// required fields so individual tests can override just the field under test.
function valid(): FormState {
  const base = defaultFormState()
  return {
    ...base,
    identity: { name: 'test-agent', description: '', folder: '' },
    trigger: { type: 'webhook', auth: 'hmac' },
    capabilities: {
      tools: [
        {
          toolId: 'srv.do_thing',
          serverId: 'srv',
          serverName: 'srv',
          name: 'do_thing',
          description: '',
          approvalRequired: false,
          approvalTimeout: '',
        },
      ],
      feedback: { enabled: false, timeout: '', onTimeout: 'fail' },
    },
    task: { task: 'Do something useful.' },
    model: { provider: 'anthropic', model: 'claude-sonnet-4-6' },
    limits: { max_tokens_per_run: 20000, max_tool_calls_per_run: 50 },
    concurrency: { concurrency: 'skip', queueDepth: 0 },
  }
}

function findIssue(issues: FormIssue[], field: string, msgSubstr: string): boolean {
  return issues.some(iss => iss.field === field && iss.message.includes(msgSubstr))
}

// ---- parseGoDuration ----

describe('parseGoDuration', () => {
  it('parses minutes', () => {
    expect(parseGoDuration('5m')).toBe(5 * 60 * 1000)
  })
  it('parses hours', () => {
    expect(parseGoDuration('1h')).toBe(60 * 60 * 1000)
  })
  it('parses combined h+m', () => {
    expect(parseGoDuration('1h30m')).toBe(90 * 60 * 1000)
  })
  it('parses seconds', () => {
    expect(parseGoDuration('30s')).toBe(30 * 1000)
  })
  it('returns null for empty string', () => {
    expect(parseGoDuration('')).toBeNull()
  })
  it('returns null for invalid input', () => {
    expect(parseGoDuration('bad')).toBeNull()
  })
})

// ---- validateFormState ----

describe('validateFormState — valid baseline', () => {
  it('returns no issues for a fully valid form', () => {
    expect(validateFormState(valid())).toHaveLength(0)
  })
})

describe('validateFormState — name', () => {
  it('reports missing name', () => {
    const s = valid()
    s.identity = { ...s.identity, name: '' }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'name', 'name is required')).toBe(true)
  })
  it('reports whitespace-only name', () => {
    const s = valid()
    s.identity = { ...s.identity, name: '   ' }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'name', 'name is required')).toBe(true)
  })
})

describe('validateFormState — trigger.fire_at', () => {
  it('reports missing fire_at for scheduled trigger', () => {
    const s = valid()
    s.trigger = { type: 'scheduled', fireAt: [] }
    const issues = validateFormState(s)
    // scheduled with empty fireAt AND empty capabilities triggers two issues;
    // we only care that the fire_at one is present.
    expect(findIssue(issues, 'trigger.fire_at', 'required')).toBe(true)
  })
  it('no fire_at error when timestamps are provided', () => {
    const s = valid()
    s.trigger = { type: 'scheduled', fireAt: ['2030-01-01T00:00:00Z'] }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'trigger.fire_at', 'required')).toBe(false)
  })
})

describe('validateFormState — trigger.interval', () => {
  it('reports missing interval for poll trigger', () => {
    const s = valid()
    s.trigger = { type: 'poll', interval: '', match: 'all', checks: [{ tool: 'srv.t', input: '', path: '$.x', comparator: 'equals', value: 'ok' }] }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'trigger.interval', 'required')).toBe(true)
  })
  it('reports interval < 1m', () => {
    const s = valid()
    s.trigger = { type: 'poll', interval: '30s', match: 'all', checks: [{ tool: 'srv.t', input: '', path: '$.x', comparator: 'equals', value: 'ok' }] }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'trigger.interval', 'at least 1m')).toBe(true)
  })
  it('no interval error for valid duration', () => {
    const s = valid()
    s.trigger = { type: 'poll', interval: '5m', match: 'all', checks: [{ tool: 'srv.t', input: '', path: '$.x', comparator: 'equals', value: 'ok' }] }
    expect(findIssue(validateFormState(s), 'trigger.interval', 'required')).toBe(false)
  })
})

describe('validateFormState — trigger.checks', () => {
  it('reports missing tool in poll check', () => {
    const s = valid()
    s.trigger = { type: 'poll', interval: '5m', match: 'all', checks: [{ tool: '', input: '', path: '$.x', comparator: 'equals', value: 'ok' }] }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'trigger.checks[0].tool', 'required')).toBe(true)
  })
  it('reports bad dot-notation in poll check tool', () => {
    const s = valid()
    s.trigger = { type: 'poll', interval: '5m', match: 'all', checks: [{ tool: 'nodot', input: '', path: '$.x', comparator: 'equals', value: 'ok' }] }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'trigger.checks[0].tool', 'dot notation')).toBe(true)
  })
  it('reports missing path in poll check', () => {
    const s = valid()
    s.trigger = { type: 'poll', interval: '5m', match: 'all', checks: [{ tool: 'srv.t', input: '', path: '', comparator: 'equals', value: 'ok' }] }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'trigger.checks[0].path', 'required')).toBe(true)
  })
  it('reports invalid comparator in poll check', () => {
    const s = valid()
    s.trigger = {
      type: 'poll', interval: '5m', match: 'all',
      checks: [{ tool: 'srv.t', input: '', path: '$.x', comparator: 'banana' as never, value: 'ok' }],
    }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'trigger.checks[0].comparator', 'comparator')).toBe(true)
  })
})

describe('validateFormState — capabilities', () => {
  it('reports missing capability when tools and feedback both absent', () => {
    const s = valid()
    s.capabilities = { tools: [], feedback: { enabled: false, timeout: '', onTimeout: 'fail' } }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'capabilities', 'at least one capability')).toBe(true)
  })
  it('no capability error when feedback is enabled', () => {
    const s = valid()
    s.capabilities = { tools: [], feedback: { enabled: true, timeout: '', onTimeout: 'fail' } }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'capabilities', 'at least one capability')).toBe(false)
  })
  it('reports duplicate tool', () => {
    const s = valid()
    s.capabilities.tools = [
      { toolId: 'srv.t', serverId: 'srv', serverName: 'srv', name: 't', description: '', approvalRequired: false, approvalTimeout: '' },
      { toolId: 'srv.t', serverId: 'srv', serverName: 'srv', name: 't', description: '', approvalRequired: false, approvalTimeout: '' },
    ]
    const issues = validateFormState(s)
    expect(findIssue(issues, 'capabilities.tools[1].tool', 'duplicate')).toBe(true)
  })
  it('reports invalid approval timeout', () => {
    const s = valid()
    s.capabilities.tools = [
      { toolId: 'srv.t', serverId: 'srv', serverName: 'srv', name: 't', description: '', approvalRequired: true, approvalTimeout: 'badtime' },
    ]
    const issues = validateFormState(s)
    expect(findIssue(issues, 'capabilities.tools[0].timeout', 'not a valid duration')).toBe(true)
  })
  it('reports invalid feedback timeout', () => {
    const s = valid()
    s.capabilities.feedback = { enabled: true, timeout: 'badtime', onTimeout: 'fail' }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'capabilities.feedback.timeout', 'not a valid duration')).toBe(true)
  })
})

describe('validateFormState — agent.task', () => {
  it('reports empty task', () => {
    const s = valid()
    s.task = { task: '' }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'agent.task', 'required')).toBe(true)
  })
})

describe('validateFormState — model', () => {
  it('reports missing provider', () => {
    const s = valid()
    s.model = { provider: '', model: 'claude-sonnet-4-6' }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'model.provider', 'required')).toBe(true)
  })
  it('reports missing model name', () => {
    const s = valid()
    s.model = { provider: 'anthropic', model: '' }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'model.name', 'required')).toBe(true)
  })
})

describe('validateFormState — agent.limits', () => {
  it('reports non-positive max_tokens_per_run', () => {
    const s = valid()
    s.limits = { max_tokens_per_run: 0, max_tool_calls_per_run: 50 }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'agent.limits.max_tokens_per_run', 'must be positive')).toBe(true)
  })
  it('reports non-positive max_tool_calls_per_run', () => {
    const s = valid()
    s.limits = { max_tokens_per_run: 1000, max_tool_calls_per_run: 0 }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'agent.limits.max_tool_calls_per_run', 'must be positive')).toBe(true)
  })
})

describe('validateFormState — agent.concurrency', () => {
  it('reports invalid concurrency mode', () => {
    const s = valid()
    s.concurrency = { concurrency: 'invalid' as never, queueDepth: 0 }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'agent.concurrency', 'invalid')).toBe(true)
  })
  it('reports negative queue depth', () => {
    const s = valid()
    s.concurrency = { concurrency: 'queue', queueDepth: -1 }
    const issues = validateFormState(s)
    expect(findIssue(issues, 'agent.queue_depth', 'must not be negative')).toBe(true)
  })
})

describe('validateFormState — replace + approval cross-field rule', () => {
  it('reports replace + approval conflict', () => {
    const s = valid()
    s.concurrency = { concurrency: 'replace', queueDepth: 0 }
    s.capabilities.tools = [
      { toolId: 'srv.t', serverId: 'srv', serverName: 'srv', name: 't', description: '', approvalRequired: true, approvalTimeout: '' },
    ]
    const issues = validateFormState(s)
    expect(findIssue(issues, 'agent.concurrency', '"replace" is not valid')).toBe(true)
  })
  it('no replace+approval error when no tool has approval required', () => {
    const s = valid()
    s.concurrency = { concurrency: 'replace', queueDepth: 0 }
    s.capabilities.tools = [
      { toolId: 'srv.t', serverId: 'srv', serverName: 'srv', name: 't', description: '', approvalRequired: false, approvalTimeout: '' },
    ]
    const issues = validateFormState(s)
    expect(findIssue(issues, 'agent.concurrency', '"replace"')).toBe(false)
  })
})
