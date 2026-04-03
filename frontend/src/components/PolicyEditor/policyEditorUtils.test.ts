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
    expect(state!.model.provider).toBe('anthropic')
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

  it('defaults cron trigger type to webhook (removed trigger type)', () => {
    const state = yamlToFormState('name: p\ntrigger:\n  type: cron\n  schedule: "0 * * * *"\n')
    expect(state!.trigger.type).toBe('webhook')
  })

  it('defaults poll trigger type to webhook (removed trigger type)', () => {
    const state = yamlToFormState('name: p\ntrigger:\n  type: poll\n  interval: 10m\n')
    expect(state!.trigger.type).toBe('webhook')
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
  it('parses tools with server.tool format', () => {
    const yaml = `name: p
capabilities:
  tools:
    - tool: myserver.read_file
`
    const state = yamlToFormState(yaml)
    expect(state!.capabilities.tools).toHaveLength(1)
    const tool = state!.capabilities.tools[0]
    expect(tool.serverId).toBe('myserver')
    expect(tool.serverName).toBe('myserver')
    expect(tool.name).toBe('read_file')
    expect(tool.approvalRequired).toBe(false)
  })

  it('parses tools with approval: required flag', () => {
    const yaml = `name: p
capabilities:
  tools:
    - tool: myserver.write_file
      approval: required
`
    const state = yamlToFormState(yaml)
    expect(state!.capabilities.tools).toHaveLength(1)
    const tool = state!.capabilities.tools[0]
    expect(tool.approvalRequired).toBe(true)
  })

  it('parses tool without approval: required as approvalRequired false', () => {
    const yaml = `name: p
capabilities:
  tools:
    - tool: myserver.deploy
`
    const state = yamlToFormState(yaml)
    const tool = state!.capabilities.tools[0]
    expect(tool.approvalRequired).toBe(false)
  })

  it('preserves _toolExtras (timeout and on_timeout)', () => {
    const yaml = `name: p
capabilities:
  tools:
    - tool: myserver.deploy
      timeout: 300
      on_timeout: fail
`
    const state = yamlToFormState(yaml)
    expect(state!._toolExtras).toBeDefined()
    expect(state!._toolExtras!['myserver.deploy']).toEqual({ timeout: 300, on_timeout: 'fail' })
  })

  it('parses new feedback config block with enabled, timeout, on_timeout', () => {
    const yaml = `name: p
capabilities:
  tools: []
  feedback:
    enabled: true
    timeout: 30m
    on_timeout: fail
`
    const state = yamlToFormState(yaml)
    expect(state!.capabilities.feedback.enabled).toBe(true)
    expect(state!.capabilities.feedback.timeout).toBe('30m')
    expect(state!.capabilities.feedback.onTimeout).toBe('fail')
  })

  it('parses old list feedback format as enabled: true (backward compat)', () => {
    const yaml = `name: p
capabilities:
  tools: []
  feedback:
    - server.feedback_tool
`
    const state = yamlToFormState(yaml)
    expect(state!.capabilities.feedback.enabled).toBe(true)
  })

  it('defaults absent feedback to enabled: false', () => {
    const yaml = `name: p
capabilities:
  tools: []
`
    const state = yamlToFormState(yaml)
    expect(state!.capabilities.feedback.enabled).toBe(false)
    expect(state!.capabilities.feedback.timeout).toBe('')
    expect(state!.capabilities.feedback.onTimeout).toBe('fail')
  })

  it('handles empty tools array', () => {
    const yaml = `name: p
capabilities:
  tools: []
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

  it('parses top-level model section with provider and name', () => {
    const yaml = `name: p
model:
  provider: google
  name: gemini-2.0-flash
agent:
  task: do things
`
    const state = yamlToFormState(yaml)
    expect(state!.model.provider).toBe('google')
    expect(state!.model.model).toBe('gemini-2.0-flash')
  })

  it('defaults missing model section to anthropic claude-sonnet-4-6', () => {
    const yaml = `name: p
agent:
  task: do things
`
    const state = yamlToFormState(yaml)
    expect(state!.model.provider).toBe('anthropic')
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

  it('emits feedback block when feedback.enabled is true', () => {
    const yaml = `name: p
capabilities:
  tools: []
  feedback:
    enabled: true
    timeout: 30m
    on_timeout: fail
`
    const state = yamlToFormState(yaml)!
    const output = formStateToYaml(state)
    expect(output).toContain('feedback')
    expect(output).toContain('enabled: true')
    expect(output).toContain('timeout: 30m')
    expect(output).toContain('on_timeout: fail')
  })

  it('omits feedback block when feedback.enabled is false', () => {
    const yaml = `name: p
capabilities:
  tools: []
`
    const state = yamlToFormState(yaml)!
    const output = formStateToYaml(state)
    expect(output).not.toContain('feedback')
  })

  it('preserves _toolExtras (timeout and on_timeout) in output', () => {
    const yaml = `name: p
capabilities:
  tools:
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

  it('emits approval: required in YAML when approvalRequired is true', () => {
    const yaml = `name: p
capabilities:
  tools:
    - tool: srv.deploy
      approval: required
`
    const state = yamlToFormState(yaml)!
    const output = formStateToYaml(state)
    expect(output).toContain('approval: required')
  })

  it('does not emit approval in YAML when approvalRequired is false', () => {
    const yaml = `name: p
capabilities:
  tools:
    - tool: srv.deploy
`
    const state = yamlToFormState(yaml)!
    const output = formStateToYaml(state)
    expect(output).not.toContain('approval: required')
  })
})

// --- Round-trip fidelity ---

describe('round-trip fidelity', () => {
  const COMPREHENSIVE_YAML = `name: comprehensive-policy
description: A comprehensive policy
folder: ops
model:
  provider: anthropic
  name: claude-opus-4-6
trigger:
  type: webhook
capabilities:
  tools:
    - tool: github.list_prs
    - tool: github.merge_pr
      approval: required
      timeout: 300
      on_timeout: fail
  feedback:
    enabled: true
    timeout: 30m
    on_timeout: fail
agent:
  preamble: "You are a GitHub automation agent."
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
    expect(second!.model.provider).toBe(first!.model.provider)
    expect(second!.model.model).toBe(first!.model.model)
    expect(second!._preamble).toBe(first!._preamble)
    expect(second!.capabilities.feedback.enabled).toBe(first!.capabilities.feedback.enabled)
    expect(second!.capabilities.feedback.timeout).toBe(first!.capabilities.feedback.timeout)

    expect(second!.capabilities.tools).toHaveLength(first!.capabilities.tools.length)
    first!.capabilities.tools.forEach((t, i) => {
      expect(second!.capabilities.tools[i].serverName).toBe(t.serverName)
      expect(second!.capabilities.tools[i].name).toBe(t.name)
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

})

// --- defaultFormState ---

describe('defaultFormState', () => {
  it('returns a valid FormState matching DEFAULT_YAML expectations', () => {
    const state = defaultFormState()
    // DEFAULT_YAML has empty name, webhook trigger, empty capabilities
    expect(state.identity.name).toBe('')
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
