import { describe, it, expect } from 'vitest'
import { yamlToFormState, formStateToYaml, defaultFormState, DEFAULT_YAML } from './agentEditorUtils'

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
    expect(state!.concurrency.queueDepth).toBe(0)
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

  it('parses webhook trigger with auth: hmac', () => {
    const state = yamlToFormState('name: p\ntrigger:\n  type: webhook\n  auth: hmac\n')
    if (state!.trigger.type !== 'webhook') throw new Error('expected webhook')
    expect(state!.trigger.auth).toBe('hmac')
  })

  it('parses webhook trigger with auth: bearer', () => {
    const state = yamlToFormState('name: p\ntrigger:\n  type: webhook\n  auth: bearer\n')
    if (state!.trigger.type !== 'webhook') throw new Error('expected webhook')
    expect(state!.trigger.auth).toBe('bearer')
  })

  it('parses webhook trigger with auth: none', () => {
    const state = yamlToFormState('name: p\ntrigger:\n  type: webhook\n  auth: none\n')
    if (state!.trigger.type !== 'webhook') throw new Error('expected webhook')
    expect(state!.trigger.auth).toBe('none')
  })

  it('defaults webhook auth to none when auth is absent and no legacy secret', () => {
    // Backend grandfathers absent auth + absent secret to none (not hmac).
    // This test ensures the frontend matches that behaviour so legacy unauthenticated
    // webhooks are not silently upgraded to hmac on first save.
    const state = yamlToFormState('name: p\ntrigger:\n  type: webhook\n')
    if (state!.trigger.type !== 'webhook') throw new Error('expected webhook')
    expect(state!.trigger.auth).toBe('none')
  })

  it('defaults webhook auth to hmac when auth is absent but legacy webhook_secret is present', () => {
    // Legacy policies had webhook_secret in YAML but no auth field. The grandfathering
    // rule preserves their security posture by defaulting to hmac.
    const state = yamlToFormState(
      'name: p\ntrigger:\n  type: webhook\n  webhook_secret: "abc123"\n',
    )
    if (state!.trigger.type !== 'webhook') throw new Error('expected webhook')
    expect(state!.trigger.auth).toBe('hmac')
  })

  it('defaults webhook auth to none when auth is invalid and no legacy secret', () => {
    // Invalid auth value with no legacy secret → none (matches backend default).
    const state = yamlToFormState('name: p\ntrigger:\n  type: webhook\n  auth: invalid\n')
    if (state!.trigger.type !== 'webhook') throw new Error('expected webhook')
    expect(state!.trigger.auth).toBe('none')
  })

  it('defaults cron trigger type to webhook (unsupported trigger type)', () => {
    const state = yamlToFormState('name: p\ntrigger:\n  type: cron\n  schedule: "0 * * * *"\n')
    expect(state!.trigger.type).toBe('webhook')
  })

  it('parses poll trigger with checks', () => {
    const yaml = `name: p
trigger:
  type: poll
  interval: 5m
  match: any
  checks:
    - tool: srv.check
      path: "$.status"
      equals: degraded
`
    const state = yamlToFormState(yaml)
    expect(state!.trigger.type).toBe('poll')
    if (state!.trigger.type !== 'poll') throw new Error('expected poll')
    expect(state!.trigger.interval).toBe('5m')
    expect(state!.trigger.match).toBe('any')
    expect(state!.trigger.checks).toHaveLength(1)
    expect(state!.trigger.checks[0].tool).toBe('srv.check')
    expect(state!.trigger.checks[0].path).toBe('$.status')
    expect(state!.trigger.checks[0].comparator).toBe('equals')
    expect(state!.trigger.checks[0].value).toBe('degraded')
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
    expect(tool.approvalTimeout).toBe('')
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

  it('reads string timeout directly into approvalTimeout (string pass-through)', () => {
    const yaml = `name: p
capabilities:
  tools:
    - tool: myserver.deploy
      approval: required
      timeout: 30m
`
    const state = yamlToFormState(yaml)
    expect(state!.capabilities.tools[0].approvalTimeout).toBe('30m')
  })

  it('converts numeric timeout to Go duration string (numeric branch: 300 → "300s")', () => {
    // js-yaml hands us a number for bare-integer values like `timeout: 300`.
    // The backend requires a Go duration string — bare integers fail time.ParseDuration.
    // We convert number n to "${n}s" so the round-trip emits a valid duration.
    const yaml = `name: p
capabilities:
  tools:
    - tool: myserver.deploy
      approval: required
      timeout: 300
`
    const state = yamlToFormState(yaml)
    expect(state!.capabilities.tools[0].approvalTimeout).toBe('300s')
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
  it('parses top-level model section with provider and name', () => {
    const yaml = `name: p
model:
  provider: google
  name: gemini-2.5-flash
agent:
  task: do things
`
    const state = yamlToFormState(yaml)
    expect(state!.model.provider).toBe('google')
    expect(state!.model.model).toBe('gemini-2.5-flash')
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

  it('parses agent.queue_depth into concurrency.queueDepth', () => {
    const yaml = `name: p
agent:
  concurrency: queue
  queue_depth: 5
  task: do things
`
    const state = yamlToFormState(yaml)
    expect(state!.concurrency.queueDepth).toBe(5)
  })

  it('defaults queueDepth to 0 when queue_depth is absent', () => {
    const yaml = `name: p
agent:
  concurrency: queue
  task: do things
`
    const state = yamlToFormState(yaml)
    expect(state!.concurrency.queueDepth).toBe(0)
  })
})

// --- Serialization ---

describe('formStateToYaml — serialization', () => {
  it('serializes webhook auth mode into YAML', () => {
    const state = yamlToFormState('name: p\ntrigger:\n  type: webhook\n  auth: bearer\n')!
    const yaml = formStateToYaml(state)
    expect(yaml).toContain('auth: bearer')
  })

  it('serializes webhook auth: none (absent auth, no legacy secret) into YAML', () => {
    // Absent auth with no legacy secret defaults to none — the serialised YAML
    // must preserve that so a re-save does not change the effective auth mode.
    const state = yamlToFormState('name: p\ntrigger:\n  type: webhook\n')!
    const yaml = formStateToYaml(state)
    expect(yaml).toContain('auth: none')
  })

  it('serializes webhook auth: hmac when explicitly set', () => {
    const state = yamlToFormState('name: p\ntrigger:\n  type: webhook\n  auth: hmac\n')!
    const yaml = formStateToYaml(state)
    expect(yaml).toContain('auth: hmac')
  })

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

  it('serializes poll trigger with checks', () => {
    const yaml = `name: p
trigger:
  type: poll
  interval: 10m
  match: all
  checks:
    - tool: srv.check
      path: "$.count"
      greater_than: 5
`
    const state = yamlToFormState(yaml)!
    const output = formStateToYaml(state)
    expect(output).toContain('type: poll')
    expect(output).toContain('interval: 10m')
    expect(output).toContain('match: all')
    expect(output).toContain('srv.check')
    expect(output).toContain('greater_than')
    // The number 5 should round-trip as a number not a quoted string
    expect(output).toContain('5')
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

  it('does not emit preamble — preamble is no longer a per-policy field', () => {
    // Existing policies with preamble: in YAML are silently migrated on first save
    // (preamble field is dropped). This is intentional per the issue.
    const yaml = `name: p
agent:
  preamble: "System context."
  task: do things
`
    const state = yamlToFormState(yaml)!
    const output = formStateToYaml(state)
    expect(output).not.toContain('preamble')
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

  it('emits approval: required, timeout, and on_timeout: reject when approvalRequired and approvalTimeout are set', () => {
    const yaml = `name: p
capabilities:
  tools:
    - tool: srv.deploy
      approval: required
      timeout: 30m
`
    const state = yamlToFormState(yaml)!
    const output = formStateToYaml(state)
    expect(output).toContain('approval: required')
    expect(output).toContain('timeout: 30m')
    expect(output).toContain('on_timeout: reject')
  })

  it('does not emit approval, timeout, or on_timeout when approvalRequired is false (even if approvalTimeout is set)', () => {
    // State preservation rule: approvalTimeout stays in form state when toggled off,
    // but the serializer must not emit it.
    const state = yamlToFormState(`name: p
capabilities:
  tools:
    - tool: srv.deploy
      approval: required
      timeout: 30m
`)!
    // Toggle approval off — approvalTimeout is preserved in state per the spec
    const modified = {
      ...state,
      capabilities: {
        ...state.capabilities,
        tools: state.capabilities.tools.map(t => ({ ...t, approvalRequired: false })),
      },
    }
    const output = formStateToYaml(modified)
    expect(output).not.toContain('approval:')
    expect(output).not.toContain('timeout:')
    expect(output).not.toContain('on_timeout:')
  })

  it('emits approval: required without timeout when approvalTimeout is empty', () => {
    const yaml = `name: p
capabilities:
  tools:
    - tool: srv.deploy
      approval: required
`
    const state = yamlToFormState(yaml)!
    const output = formStateToYaml(state)
    expect(output).toContain('approval: required')
    expect(output).not.toContain('timeout:')
    expect(output).not.toContain('on_timeout:')
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

  it('emits queue_depth when mode is queue and queueDepth > 0', () => {
    const yaml = `name: p
agent:
  concurrency: queue
  queue_depth: 5
  task: do things
`
    const state = yamlToFormState(yaml)!
    const output = formStateToYaml(state)
    expect(output).toContain('queue_depth: 5')
  })

  it('omits queue_depth when queueDepth is 0', () => {
    const yaml = `name: p
agent:
  concurrency: queue
  task: do things
`
    const state = yamlToFormState(yaml)!
    const output = formStateToYaml(state)
    expect(output).not.toContain('queue_depth')
  })

  it('omits queue_depth when concurrency mode is not queue even if queueDepth > 0', () => {
    const yaml = `name: p
agent:
  concurrency: queue
  queue_depth: 5
  task: do things
`
    const state = yamlToFormState(yaml)!
    // Switch mode to skip
    const modified = { ...state, concurrency: { ...state.concurrency, concurrency: 'skip' as const } }
    const output = formStateToYaml(modified)
    expect(output).not.toContain('queue_depth')
  })
})

// --- Round-trip fidelity ---

describe('round-trip fidelity', () => {
  // Uses a valid Go duration string (30m) instead of bare integer (300).
  // Bare integers fail backend time.ParseDuration.
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
      timeout: 30m
  feedback:
    enabled: true
    timeout: 30m
    on_timeout: fail
agent:
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
    expect(second!.capabilities.feedback.enabled).toBe(first!.capabilities.feedback.enabled)
    expect(second!.capabilities.feedback.timeout).toBe(first!.capabilities.feedback.timeout)

    expect(second!.capabilities.tools).toHaveLength(first!.capabilities.tools.length)

    // Gated tool should preserve approvalTimeout string pass-through (30m → 30m)
    const gatedTool = second!.capabilities.tools.find(t => t.approvalRequired)
    expect(gatedTool).toBeDefined()
    expect(gatedTool!.approvalTimeout).toBe('30m')

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

  it('round-trips poll trigger preserving interval, match, and checks', () => {
    const yaml = `name: p
trigger:
  type: poll
  interval: 5m
  match: any
  checks:
    - tool: srv.check
      path: "$.status"
      equals: degraded
    - tool: srv.check
      path: "$.count"
      greater_than: 10
`
    const first = yamlToFormState(yaml)!
    const second = yamlToFormState(formStateToYaml(first))!
    expect(second.trigger.type).toBe('poll')
    if (second.trigger.type !== 'poll') throw new Error('expected poll')
    expect(second.trigger.interval).toBe('5m')
    expect(second.trigger.match).toBe('any')
    expect(second.trigger.checks).toHaveLength(2)
    expect(second.trigger.checks[0].tool).toBe('srv.check')
    expect(second.trigger.checks[0].comparator).toBe('equals')
    expect(second.trigger.checks[0].value).toBe('degraded')
    expect(second.trigger.checks[1].comparator).toBe('greater_than')
    expect(second.trigger.checks[1].value).toBe('10')
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

  it('round-trips concurrency queue + queueDepth 5', () => {
    const yaml = `name: p
agent:
  concurrency: queue
  queue_depth: 5
  task: do things
`
    const first = yamlToFormState(yaml)!
    const second = yamlToFormState(formStateToYaml(first))!
    expect(second.concurrency.concurrency).toBe('queue')
    expect(second.concurrency.queueDepth).toBe(5)
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
    expect(state.concurrency.queueDepth).toBe(0)
  })

  it('matches what yamlToFormState(DEFAULT_YAML) returns', () => {
    const fromDefault = defaultFormState()
    const fromParsed = yamlToFormState(DEFAULT_YAML)
    expect(fromDefault).toEqual(fromParsed)
  })
})
