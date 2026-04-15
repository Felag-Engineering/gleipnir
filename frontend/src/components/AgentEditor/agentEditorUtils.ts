import { dump, load } from 'js-yaml'
import type {
  CapabilitiesFormState,
  ConcurrencyFormState,
  ConcurrencyValue,
  FeedbackFormState,
  IdentityFormState,
  ModelFormState,
  RunLimitsFormState,
  TaskInstructionsFormState,
  TriggerFormState,
  WebhookAuthMode,
} from '@/components/AgentEditor/FormMode/types'

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

export interface FormState {
  identity: IdentityFormState
  trigger: TriggerFormState
  capabilities: CapabilitiesFormState
  task: TaskInstructionsFormState
  limits: RunLimitsFormState
  concurrency: ConcurrencyFormState
  model: ModelFormState
}

export const DEFAULT_YAML = `name: ''
description: ''
model:
  provider: anthropic
  name: claude-sonnet-4-6
trigger:
  type: webhook
capabilities:
  tools: []
agent:
  task: |
    Describe what this agent should do.
  limits:
    max_tokens_per_run: 20000
    max_tool_calls_per_run: 50
  concurrency: skip
`

// yamlToFormState parses a YAML string into FormState. Returns null if YAML is
// malformed or the parsed value is not an object.
export function yamlToFormState(yaml: string): FormState | null {
  let parsed: unknown
  try {
    parsed = load(yaml)
  } catch {
    return null
  }

  if (!isRecord(parsed)) {
    return null
  }

  const p = parsed

  // Identity
  const identity: IdentityFormState = {
    name: typeof p.name === 'string' ? p.name : '',
    description: typeof p.description === 'string' ? p.description : '',
    folder: typeof p.folder === 'string' ? p.folder : '',
  }

  // Trigger
  const triggerRaw = isRecord(p.trigger) ? p.trigger : {}

  let trigger: TriggerFormState
  const triggerType = triggerRaw.type
  if (triggerType === 'manual') {
    trigger = { type: 'manual' }
  } else if (triggerType === 'scheduled') {
    const fireAtRaw = Array.isArray(triggerRaw.fire_at) ? triggerRaw.fire_at : []
    trigger = {
      type: 'scheduled',
      fireAt: fireAtRaw.filter((v: unknown) => typeof v === 'string') as string[],
    }
  } else if (triggerType === 'poll') {
    const interval = typeof triggerRaw.interval === 'string'
      ? triggerRaw.interval
      : typeof triggerRaw.interval === 'number'
        ? `${triggerRaw.interval}m`
        : '5m'
    const match = triggerRaw.match === 'any' ? 'any' : 'all'
    const checksRaw = Array.isArray(triggerRaw.checks) ? triggerRaw.checks : []
    const comparators = ['equals', 'not_equals', 'greater_than', 'less_than', 'contains']
    const checks = checksRaw.flatMap((c: unknown) => {
      if (!isRecord(c)) return []
      const tool = typeof c.tool === 'string' ? c.tool : ''
      const input = isRecord(c.input) ? JSON.stringify(c.input) : ''
      const path = typeof c.path === 'string' ? c.path : ''
      let comparator = 'equals'
      let value = ''
      for (const comp of comparators) {
        if (c[comp] !== undefined && c[comp] !== null) {
          comparator = comp
          value = String(c[comp])
          break
        }
      }
      return [{ tool, input, path, comparator: comparator as 'equals' | 'not_equals' | 'greater_than' | 'less_than' | 'contains', value }]
    })
    trigger = {
      type: 'poll',
      interval,
      match,
      checks: checks.length > 0
        ? checks
        : [{ tool: '', input: '', path: '', comparator: 'equals' as const, value: '' }],
    }
  } else {
    // Parse auth mode. If auth is explicitly set, use it. Otherwise fall back
    // to the backend grandfathering rule: a pre-existing in-YAML webhook_secret
    // (now deprecated) implies hmac; absent secret means none. This prevents the
    // form from silently upgrading legacy unauthenticated webhooks to hmac on save.
    const validAuthModes: WebhookAuthMode[] = ['hmac', 'bearer', 'none']
    const rawAuth = triggerRaw.auth
    let auth: WebhookAuthMode
    if (validAuthModes.includes(rawAuth as WebhookAuthMode)) {
      auth = rawAuth as WebhookAuthMode
    } else if (typeof triggerRaw.webhook_secret === 'string' && triggerRaw.webhook_secret !== '') {
      // Legacy policy: had a secret in YAML but no auth field — grandfathered to hmac.
      auth = 'hmac'
    } else {
      // No auth field and no legacy secret: default to none (matches backend).
      auth = 'none'
    }
    trigger = { type: 'webhook', auth }
  }

  // Capabilities
  const capsRaw = isRecord(p.capabilities) ? p.capabilities : {}

  const tools = Array.isArray(capsRaw.tools)
    ? capsRaw.tools.flatMap((entry: unknown) => {
        if (!isRecord(entry)) return []
        const e = entry
        const toolStr = typeof e.tool === 'string' ? e.tool : ''
        if (!toolStr) return []
        const dotIdx = toolStr.indexOf('.')
        const serverPart = dotIdx >= 0 ? toolStr.slice(0, dotIdx) : toolStr
        const toolPart = dotIdx >= 0 ? toolStr.slice(dotIdx + 1) : ''
        const approvalRequired = e.approval === 'required'

        // Convert timeout to a Go duration string. js-yaml may hand us a number
        // for bare-integer values in legacy YAML (e.g. `timeout: 300`), which
        // would fail time.ParseDuration on the backend. Convert those to "${n}s".
        let approvalTimeout = ''
        if (typeof e.timeout === 'string') {
          approvalTimeout = e.timeout
        } else if (typeof e.timeout === 'number') {
          approvalTimeout = `${e.timeout}s`
        }

        return [{
          toolId: toolStr,
          serverId: serverPart,
          serverName: serverPart,
          name: toolPart,
          description: '',
          role: 'tool' as const, // placeholder — CapabilitiesSection reconciles with registry
          approvalRequired,
          approvalTimeout,
        }]
      })
    : []

  // Parse feedback config block. Supports:
  //   - new format: { enabled: true, timeout: "30m", on_timeout: "fail" }
  //   - old list format: ["server.tool"] → treated as enabled: true (backward compat)
  //   - absent/null → disabled
  const feedbackRaw = capsRaw.feedback
  let feedback: FeedbackFormState
  if (Array.isArray(feedbackRaw)) {
    // Old list format — backward compat, treat as enabled with no extras
    feedback = { enabled: true, timeout: '', onTimeout: 'fail' }
  } else if (isRecord(feedbackRaw)) {
    const fb = feedbackRaw
    feedback = {
      enabled: fb.enabled === true,
      timeout: typeof fb.timeout === 'string' ? fb.timeout : '',
      onTimeout: typeof fb.on_timeout === 'string' ? fb.on_timeout : 'fail',
    }
  } else {
    feedback = { enabled: false, timeout: '', onTimeout: 'fail' }
  }

  const capabilities: CapabilitiesFormState = { tools, feedback }

  // Agent block
  const agentRaw = isRecord(p.agent) ? p.agent : {}

  const limitsRaw = isRecord(agentRaw.limits) ? agentRaw.limits : {}

  const task: TaskInstructionsFormState = {
    task: typeof agentRaw.task === 'string' ? agentRaw.task : '',
  }

  const limits: RunLimitsFormState = {
    max_tokens_per_run: typeof limitsRaw.max_tokens_per_run === 'number' ? limitsRaw.max_tokens_per_run : 20000,
    max_tool_calls_per_run: typeof limitsRaw.max_tool_calls_per_run === 'number' ? limitsRaw.max_tool_calls_per_run : 50,
  }

  const concurrencyRaw = agentRaw.concurrency
  const validConcurrency: ConcurrencyValue[] = ['skip', 'queue', 'parallel', 'replace']
  const concurrency: ConcurrencyFormState = {
    concurrency: validConcurrency.includes(concurrencyRaw as ConcurrencyValue)
      ? (concurrencyRaw as ConcurrencyValue)
      : 'skip',
    queueDepth: typeof agentRaw.queue_depth === 'number' ? agentRaw.queue_depth : 0,
  }

  // Read model from top-level model: section
  const modelRaw = isRecord(p.model) ? p.model : null

  const model: ModelFormState = modelRaw && typeof modelRaw.provider === 'string' && typeof modelRaw.name === 'string'
    ? { provider: modelRaw.provider, model: modelRaw.name }
    : { provider: 'anthropic', model: 'claude-sonnet-4-6' }

  return {
    identity,
    trigger,
    capabilities,
    task,
    limits,
    concurrency,
    model,
  }
}

// formStateToYaml serializes FormState back to a YAML string.
export function formStateToYaml(state: FormState): string {
  const { identity, trigger, capabilities, task, limits, concurrency, model } = state

  // Build trigger object
  let triggerObj: Record<string, unknown>
  if (trigger.type === 'manual') {
    triggerObj = { type: 'manual' }
  } else if (trigger.type === 'scheduled') {
    triggerObj = { type: 'scheduled', fire_at: trigger.fireAt }
  } else if (trigger.type === 'poll') {
    const checks = trigger.checks.map(c => {
      const entry: Record<string, unknown> = { tool: c.tool }
      if (c.input) {
        try { entry.input = JSON.parse(c.input) } catch { /* leave input out if unparseable */ }
      }
      entry.path = c.path
      // Coerce value string to number or bool where applicable so YAML types round-trip
      let parsedValue: unknown = c.value
      const num = Number(c.value)
      if (c.value !== '' && !isNaN(num)) parsedValue = num
      else if (c.value === 'true') parsedValue = true
      else if (c.value === 'false') parsedValue = false
      entry[c.comparator] = parsedValue
      return entry
    })
    triggerObj = { type: 'poll', interval: trigger.interval, match: trigger.match, checks }
  } else {
    triggerObj = { type: 'webhook', auth: trigger.auth }
  }

  // Build capabilities — single tools array
  const tools = capabilities.tools.map(t => {
    const entry: Record<string, unknown> = { tool: `${t.serverName}.${t.name}` }
    if (t.approvalRequired) {
      entry.approval = 'required'
      // Only emit timeout and on_timeout when approval is on and a timeout is set.
      // When approval is off, the timeout value is preserved in form state but not
      // serialized — this lets users toggle approval without losing their typed value.
      if (t.approvalTimeout) {
        entry.timeout = t.approvalTimeout
        entry.on_timeout = 'reject' // hardcoded — reject is the only valid value
      }
    }
    return entry
  })

  const capsObj: Record<string, unknown> = { tools }
  // Emit the feedback block only when enabled — omitting it means disabled,
  // following the same pattern as other optional fields (description, folder).
  if (capabilities.feedback.enabled) {
    const feedbackObj: Record<string, unknown> = { enabled: true }
    if (capabilities.feedback.timeout) feedbackObj.timeout = capabilities.feedback.timeout
    if (capabilities.feedback.onTimeout) feedbackObj.on_timeout = capabilities.feedback.onTimeout
    capsObj.feedback = feedbackObj
  }

  // Build agent block
  const agentObj: Record<string, unknown> = {}
  agentObj.task = task.task
  agentObj.limits = {
    max_tokens_per_run: limits.max_tokens_per_run,
    max_tool_calls_per_run: limits.max_tool_calls_per_run,
  }
  agentObj.concurrency = concurrency.concurrency
  // Emit queue_depth only when mode is queue and depth is non-zero.
  // Omitting it lets the backend apply model.DefaultQueueDepth.
  if (concurrency.concurrency === 'queue' && concurrency.queueDepth > 0) {
    agentObj.queue_depth = concurrency.queueDepth
  }

  const doc: Record<string, unknown> = {
    name: identity.name,
  }
  if (identity.description) doc.description = identity.description
  if (identity.folder) doc.folder = identity.folder
  doc.model = { provider: model.provider, name: model.model }
  doc.trigger = triggerObj
  doc.capabilities = capsObj
  doc.agent = agentObj

  return dump(doc, { lineWidth: -1 })
}

// defaultFormState returns the parsed form state from DEFAULT_YAML.
export function defaultFormState(): FormState {
  // DEFAULT_YAML is valid, so this will never return null
  return yamlToFormState(DEFAULT_YAML) as FormState
}
