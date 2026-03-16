import { describe, it, expect } from 'vitest'
import { yamlToFormState, formStateToYaml, defaultFormState, DEFAULT_YAML } from './policyEditorUtils'

// --- Invalid input ---

describe('yamlToFormState — invalid input', () => {
  it('returns null for empty string', () => {
    expect(yamlToFormState('')).toBeNull()
  })

  it('returns null for numeric YAML', () => {
    expect(yamlToFormState('42')).toBeNull()
  })

  it('returns null for list YAML', () => {
    expect(yamlToFormState('- item1\n- item2')).toBeNull()
  })

  it('returns null for null YAML', () => {
    expect(yamlToFormState('null')).toBeNull()
  })

  it('returns null for syntactically invalid YAML (unterminated flow sequence)', () => {
    expect(yamlToFormState('foo: [bar\nbaz')).toBeNull()
  })
})

// --- Defaults for missing fields ---

describe('yamlToFormState — defaults for missing fields', () => {
  it('parses minimal YAML filling in all defaults', () => {
    const state = yamlToFormState('name: x')
    expect(state).not.toBeNull()
    expect(state!.identity.name).toBe('x')
    expect(state!.identity.description).toBe('')
    expect(state!.identity.folder).toBe('')
    expect(state!.capabilities.tools).toHaveLength(0)
    expect(state!.limits.max_tokens_per_run).toBe(20000)
    expect(state!.limits.max_tool_calls_per_run).toBe(50)
    expect(state!.concurrency.concurrency).toBe('skip')
    expect(state!.model.model).toBe('claude-sonnet-4-6')
    expect(state!.trigger.type).toBe('webhook')
  })
})

// --- All trigger types ---

describe('yamlToFormState — all trigger types', () => {
  it('parses webhook trigger', () => {
    const state = yamlToFormState('name: p\ntrigger:\n  type: webhook\n')
    expect(state!.trigger.type).toBe('webhook')
  })

  it('parses cron trigger with schedule', () => {
    const state = yamlToFormState('name: p\ntrigger:\n  type: cron\n  schedule: "0 * * * *"\n')
    expect(state!.trigger.type).toBe('cron')
    if (state!.trigger.type !== 'cron') throw new Error('expected cron')
    expect(state!.trigger.schedule).toBe('0 * * * *')
  })

  it('parses poll trigger with all fields including headers', () => {
    const yaml = `name: p
trigger:
  type: poll
  interval: 10m
  request:
    url: https://example.com/api
    method: POST
    headers:
      Authorization: Bearer token
      Accept: application/json
    body: '{"key":"value"}'
  filter: .items
`
    const state = yamlToFormState(yaml)
    expect(state!.trigger.type).toBe('poll')
    if (state!.trigger.type !== 'poll') throw new Error('expected poll')
    expect(state!.trigger.interval).toBe('10m')
    expect(state!.trigger.request.url).toBe('https://example.com/api')
    expect(state!.trigger.request.method).toBe('POST')
    expect(state!.trigger.request.headers).toContain('Authorization: Bearer token')
    expect(state!.trigger.request.headers).toContain('Accept: application/json')
    expect(state!.trigger.request.body).toBe('{"key":"value"}')
    expect(state!.trigger.filter).toBe('.items')
  })

  it('parses manual trigger', () => {
    const state = yamlToFormState('name: p\ntrigger:\n  type: manual\n')
    expect(state!.trigger.type).toBe('manual')
  })

  it('parses scheduled trigger with fireAt array', () => {
    const yaml = `name: p
trigger:
  type: scheduled
  fire_at:
    - "2025-01-01T00:00:00Z"
    - "2025-06-01T00:00:00Z"
`
    const state = yamlToFormState(yaml)
    expect(state!.trigger.type).toBe('scheduled')
    if (state!.trigger.type !== 'scheduled') throw new Error('expected scheduled')
    expect(state!.trigger.fireAt).toEqual(['2025-01-01T00:00:00Z', '2025-06-01T00:00:00Z'])
  })

  it('defaults unknown trigger type to webhook', () => {
    const state = yamlToFormState('name: p\ntrigger:\n  type: unknown_type\n')
    expect(state!.trigger.type).toBe('webhook')
  })
})

// --- Capabilities parsing ---

describe('yamlToFormState — capabilities parsing', () => {
  it('parses sensor tools with server.tool format', () => {
    const yaml = `name: p
capabilities:
  sensors:
    - tool: myserver.read_file
`
    const state = yamlToFormState(yaml)
    expect(state!.capabilities.tools).toHaveLength(1)
    const tool = state!.capabilities.tools[0]
    expect(tool.serverId).toBe('myserver')
    expect(tool.serverName).toBe('myserver')
    expect(tool.name).toBe('read_file')
    expect(tool.role).toBe('sensor')
    expect(tool.approvalRequired).toBe(false)
  })

  it('parses actuator tools with approval: required flag', () => {
    const yaml = `name: p
capabilities:
  actuators:
    - tool: myserver.write_file
      approval: required
`
    const state = yamlToFormState(yaml)
    expect(state!.capabilities.tools).toHaveLength(1)
    const tool = state!.capabilities.tools[0]
    expect(tool.role).toBe('actuator')
    expect(tool.approvalRequired).toBe(true)
  })

  it('parses actuator without approval: required as approvalRequired false', () => {
    const yaml = `name: p
capabilities:
  actuators:
    - tool: myserver.deploy
`
    const state = yamlToFormState(yaml)
    const tool = state!.capabilities.tools[0]
    expect(tool.approvalRequired).toBe(false)
  })

  it('preserves _actuatorExtras (timeout and on_timeout)', () => {
    const yaml = `name: p
capabilities:
  actuators:
    - tool: myserver.deploy
      timeout: 300
      on_timeout: fail
`
    const state = yamlToFormState(yaml)
    expect(state!._actuatorExtras).toBeDefined()
    expect(state!._actuatorExtras!['myserver.deploy']).toEqual({ timeout: 300, on_timeout: 'fail' })
  })

  it('preserves _feedbackCapabilities passthrough', () => {
    const yaml = `name: p
capabilities:
  sensors: []
  actuators: []
  feedback:
    - channel: slack
`
    const state = yamlToFormState(yaml)
    expect(state!._feedbackCapabilities).toBeDefined()
    expect(Array.isArray(state!._feedbackCapabilities)).toBe(true)
    expect((state!._feedbackCapabilities as unknown[])).toHaveLength(1)
  })

  it('handles empty sensor and actuator arrays', () => {
    const yaml = `name: p
capabilities:
  sensors: []
  actuators: []
`
    const state = yamlToFormState(yaml)
    expect(state!.capabilities.tools).toHaveLength(0)
  })
})

// --- Agent block ---

describe('yamlToFormState — agent block', () => {
  it('parses preamble into _preamble', () => {
    const yaml = `name: p
agent:
  preamble: "You are a helpful agent."
  task: do things
`
    const state = yamlToFormState(yaml)
    expect(state!._preamble).toBe('You are a helpful agent.')
  })

  it('parses valid model', () => {
    const yaml = `name: p
agent:
  model: claude-opus-4-6
  task: do things
`
    const state = yamlToFormState(yaml)
    expect(state!.model.model).toBe('claude-opus-4-6')
  })

  it('defaults invalid model to claude-sonnet-4-6', () => {
    const yaml = `name: p
agent:
  model: gpt-4
  task: do things
`
    const state = yamlToFormState(yaml)
    expect(state!.model.model).toBe('claude-sonnet-4-6')
  })

  it('parses valid concurrency values', () => {
    for (const val of ['skip', 'queue', 'parallel', 'replace'] as const) {
      const yaml = `name: p\nagent:\n  concurrency: ${val}\n  task: x\n`
      const state = yamlToFormState(yaml)
      expect(state!.concurrency.concurrency).toBe(val)
    }
  })

  it('defaults invalid concurrency to skip', () => {
    const yaml = `name: p
agent:
  concurrency: invalid_value
  task: do things
`
    const state = yamlToFormState(yaml)
    expect(state!.concurrency.concurrency).toBe('skip')
  })
})

// --- Serialization ---

describe('formStateToYaml — serialization', () => {
  it('omits description when empty', () => {
    const state = yamlToFormState('name: p\ntrigger:\n  type: webhook\n')!
    const yaml = formStateToYaml(state)
    expect(yaml).not.toContain('description:')
  })

  it('includes description when non-empty', () => {
    const state = yamlToFormState('name: p\ndescription: "My policy"\ntrigger:\n  type: webhook\n')!
    const yaml = formStateToYaml(state)
    expect(yaml).toContain('description:')
    expect(yaml).toContain('My policy')
  })

  it('omits folder when empty', () => {
    const state = yamlToFormState('name: p\ntrigger:\n  type: webhook\n')!
    const yaml = formStateToYaml(state)
    expect(yaml).not.toContain('folder:')
  })

  it('includes folder when non-empty', () => {
    const state = yamlToFormState('name: p\nfolder: my-folder\ntrigger:\n  type: webhook\n')!
    const yaml = formStateToYaml(state)
    expect(yaml).toContain('folder:')
    expect(yaml).toContain('my-folder')
  })

  it('serializes poll trigger headers back to object format', () => {
    const yaml = `name: p
trigger:
  type: poll
  interval: 5m
  request:
    url: https://api.example.com
    method: GET
    headers:
      Authorization: Bearer token
  filter: ''
`
    const state = yamlToFormState(yaml)!
    const output = formStateToYaml(state)
    // Headers should be serialized as a YAML mapping
    expect(output).toContain('Authorization')
    expect(output).toContain('Bearer token')
  })

  it('serializes poll body only when present', () => {
    const yamlWithBody = `name: p
trigger:
  type: poll
  interval: 5m
  request:
    url: https://api.example.com
    method: POST
    body: '{"q":"test"}'
  filter: ''
`
    const stateWithBody = yamlToFormState(yamlWithBody)!
    const outputWithBody = formStateToYaml(stateWithBody)
    expect(outputWithBody).toContain('body')

    const yamlNoBody = `name: p
trigger:
  type: poll
  interval: 5m
  request:
    url: https://api.example.com
    method: GET
  filter: ''
`
    const stateNoBody = yamlToFormState(yamlNoBody)!
    const outputNoBody = formStateToYaml(stateNoBody)
    expect(outputNoBody).not.toContain('body')
  })

  it('serializes scheduled trigger fireAt as fire_at array', () => {
    const yaml = `name: p
trigger:
  type: scheduled
  fire_at:
    - "2025-01-01T00:00:00Z"
`
    const state = yamlToFormState(yaml)!
    const output = formStateToYaml(state)
    expect(output).toContain('fire_at')
    expect(output).toContain('2025-01-01T00:00:00Z')
  })

  it('includes _preamble in agent block', () => {
    const yaml = `name: p
agent:
  preamble: "System context."
  task: do things
`
    const state = yamlToFormState(yaml)!
    const output = formStateToYaml(state)
    expect(output).toContain('preamble')
    expect(output).toContain('System context.')
  })

  it('includes _feedbackCapabilities in capabilities', () => {
    const yaml = `name: p
capabilities:
  sensors: []
  actuators: []
  feedback:
    - channel: email
`
    const state = yamlToFormState(yaml)!
    const output = formStateToYaml(state)
    expect(output).toContain('feedback')
  })

  it('preserves _actuatorExtras (timeout and on_timeout) in output', () => {
    const yaml = `name: p
capabilities:
  actuators:
    - tool: srv.deploy
      timeout: 120
      on_timeout: skip
`
    const state = yamlToFormState(yaml)!
    const output = formStateToYaml(state)
    expect(output).toContain('timeout')
    expect(output).toContain('120')
    expect(output).toContain('on_timeout')
    expect(output).toContain('skip')
  })
})

// --- Round-trip fidelity ---

describe('round-trip fidelity', () => {
  const COMPREHENSIVE_YAML = `name: comprehensive-policy
description: A comprehensive policy
folder: ops
trigger:
  type: poll
  interval: 15m
  request:
    url: https://api.example.com/events
    method: POST
    headers:
      Authorization: Bearer mytoken
      Content-Type: application/json
    body: '{"since":"2025-01-01"}'
  filter: '.events | length > 0'
capabilities:
  sensors:
    - tool: github.list_prs
  actuators:
    - tool: github.merge_pr
      approval: required
      timeout: 300
      on_timeout: fail
  feedback:
    - channel: slack
agent:
  preamble: "You are a GitHub automation agent."
  model: claude-opus-4-6
  task: |
    Process pull requests and merge when approved.
  limits:
    max_tokens_per_run: 40000
    max_tool_calls_per_run: 100
  concurrency: queue
`

  it('round-trips comprehensive YAML preserving all fields', () => {
    const first = yamlToFormState(COMPREHENSIVE_YAML)
    expect(first).not.toBeNull()
    const second = yamlToFormState(formStateToYaml(first!))
    expect(second).not.toBeNull()

    expect(second!.identity.name).toBe(first!.identity.name)
    expect(second!.identity.description).toBe(first!.identity.description)
    expect(second!.identity.folder).toBe(first!.identity.folder)
    expect(second!.trigger.type).toBe(first!.trigger.type)
    expect(second!.limits.max_tokens_per_run).toBe(first!.limits.max_tokens_per_run)
    expect(second!.limits.max_tool_calls_per_run).toBe(first!.limits.max_tool_calls_per_run)
    expect(second!.concurrency.concurrency).toBe(first!.concurrency.concurrency)
    expect(second!.model.model).toBe(first!.model.model)
    expect(second!._preamble).toBe(first!._preamble)
    expect(second!._feedbackCapabilities).toBeDefined()

    if (first!.trigger.type === 'poll' && second!.trigger.type === 'poll') {
      expect(second!.trigger.interval).toBe(first!.trigger.interval)
      expect(second!.trigger.request.url).toBe(first!.trigger.request.url)
      expect(second!.trigger.request.method).toBe(first!.trigger.request.method)
      expect(second!.trigger.filter).toBe(first!.trigger.filter)
    }

    expect(second!.capabilities.tools).toHaveLength(first!.capabilities.tools.length)
    first!.capabilities.tools.forEach((t, i) => {
      expect(second!.capabilities.tools[i].serverName).toBe(t.serverName)
      expect(second!.capabilities.tools[i].name).toBe(t.name)
      expect(second!.capabilities.tools[i].role).toBe(t.role)
      expect(second!.capabilities.tools[i].approvalRequired).toBe(t.approvalRequired)
    })
  })

  it('round-trips manual trigger', () => {
    const yaml = 'name: p\ntrigger:\n  type: manual\n'
    const first = yamlToFormState(yaml)!
    const second = yamlToFormState(formStateToYaml(first))!
    expect(second.trigger.type).toBe('manual')
  })

  it('round-trips scheduled trigger preserving fireAt array', () => {
    const yaml = `name: p
trigger:
  type: scheduled
  fire_at:
    - "2025-03-01T10:00:00Z"
    - "2025-09-01T10:00:00Z"
`
    const first = yamlToFormState(yaml)!
    const second = yamlToFormState(formStateToYaml(first))!
    expect(second.trigger.type).toBe('scheduled')
    if (second.trigger.type !== 'scheduled') throw new Error('expected scheduled')
    expect(second.trigger.fireAt).toEqual(['2025-03-01T10:00:00Z', '2025-09-01T10:00:00Z'])
  })

  it('round-trips poll trigger with headers and body', () => {
    const yaml = `name: p
trigger:
  type: poll
  interval: 5m
  request:
    url: https://api.example.com
    method: POST
    headers:
      X-Token: abc123
    body: '{"q":"search"}'
  filter: '.results'
`
    const first = yamlToFormState(yaml)!
    const second = yamlToFormState(formStateToYaml(first))!
    expect(second.trigger.type).toBe('poll')
    if (second.trigger.type !== 'poll') throw new Error('expected poll')
    expect(second.trigger.interval).toBe('5m')
    expect(second.trigger.request.url).toBe('https://api.example.com')
    expect(second.trigger.request.method).toBe('POST')
    expect(second.trigger.request.body).toBe('{"q":"search"}')
    expect(second.trigger.filter).toBe('.results')
    // Headers survive the round-trip
    expect(second.trigger.request.headers).toContain('X-Token: abc123')
  })
})

// --- defaultFormState ---

describe('defaultFormState', () => {
  it('returns a valid FormState matching DEFAULT_YAML expectations', () => {
    const state = defaultFormState()
    // DEFAULT_YAML has name: new-policy, webhook trigger, empty capabilities
    expect(state.identity.name).toBe('new-policy')
    expect(state.trigger.type).toBe('webhook')
    expect(state.capabilities.tools).toHaveLength(0)
    expect(state.limits.max_tokens_per_run).toBe(20000)
    expect(state.limits.max_tool_calls_per_run).toBe(50)
    expect(state.concurrency.concurrency).toBe('skip')
  })

  it('matches what yamlToFormState(DEFAULT_YAML) returns', () => {
    const fromDefault = defaultFormState()
    const fromParsed = yamlToFormState(DEFAULT_YAML)
    expect(fromDefault).toEqual(fromParsed)
  })
})
