import { dump, load } from 'js-yaml'
import type {
  CapabilitiesFormState,
  ConcurrencyFormState,
  ConcurrencyValue,
  IdentityFormState,
  ModelFormState,
  ModelValue,
  RunLimitsFormState,
  TaskInstructionsFormState,
  TriggerFormState,
} from '@/components/PolicyEditor/FormMode/types'

export interface FormState {
  identity: IdentityFormState
  trigger: TriggerFormState
  capabilities: CapabilitiesFormState
  task: TaskInstructionsFormState
  limits: RunLimitsFormState
  concurrency: ConcurrencyFormState
  model: ModelFormState
  // Passthrough fields preserved across round-trips but not editable in form UI
  _preamble?: string
  _actuatorExtras?: Record<string, { timeout?: number; on_timeout?: string }>
  _feedbackCapabilities?: unknown[]
}

export const DEFAULT_YAML = `name: new-policy
description: ''
trigger:
  type: webhook
capabilities:
  sensors: []
  actuators: []
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

  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return null
  }

  const p = parsed as Record<string, unknown>

  // Identity
  const identity: IdentityFormState = {
    name: typeof p.name === 'string' ? p.name : '',
    description: typeof p.description === 'string' ? p.description : '',
    folder: typeof p.folder === 'string' ? p.folder : '',
  }

  // Trigger
  const triggerRaw = p.trigger && typeof p.trigger === 'object' && !Array.isArray(p.trigger)
    ? (p.trigger as Record<string, unknown>)
    : {}

  let trigger: TriggerFormState
  const triggerType = triggerRaw.type
  if (triggerType === 'cron') {
    trigger = {
      type: 'cron',
      schedule: typeof triggerRaw.schedule === 'string' ? triggerRaw.schedule : '',
    }
  } else if (triggerType === 'manual') {
    trigger = { type: 'manual' }
  } else if (triggerType === 'scheduled') {
    const fireAtRaw = Array.isArray(triggerRaw.fire_at) ? triggerRaw.fire_at : []
    trigger = {
      type: 'scheduled',
      fireAt: fireAtRaw.filter((v: unknown) => typeof v === 'string') as string[],
    }
  } else if (triggerType === 'poll') {
    const reqRaw = triggerRaw.request && typeof triggerRaw.request === 'object' && !Array.isArray(triggerRaw.request)
      ? (triggerRaw.request as Record<string, unknown>)
      : {}

    // Headers: convert object → "Key: Value\n..." textarea string
    let headersStr = ''
    if (reqRaw.headers && typeof reqRaw.headers === 'object' && !Array.isArray(reqRaw.headers)) {
      headersStr = Object.entries(reqRaw.headers as Record<string, unknown>)
        .map(([k, v]) => `${k}: ${String(v)}`)
        .join('\n')
    }

    trigger = {
      type: 'poll',
      interval: typeof triggerRaw.interval === 'string' ? triggerRaw.interval : '5m',
      request: {
        url: typeof reqRaw.url === 'string' ? reqRaw.url : '',
        method: reqRaw.method === 'POST' ? 'POST' : 'GET',
        headers: headersStr,
        body: typeof reqRaw.body === 'string' ? reqRaw.body : undefined,
      },
      filter: typeof triggerRaw.filter === 'string' ? triggerRaw.filter : '',
    }
  } else {
    trigger = { type: 'webhook' }
  }

  // Capabilities
  const capsRaw = p.capabilities && typeof p.capabilities === 'object' && !Array.isArray(p.capabilities)
    ? (p.capabilities as Record<string, unknown>)
    : {}

  const actuatorExtras: Record<string, { timeout?: number; on_timeout?: string }> = {}

  const sensorTools = Array.isArray(capsRaw.sensors)
    ? capsRaw.sensors.flatMap((entry: unknown) => {
        if (!entry || typeof entry !== 'object' || Array.isArray(entry)) return []
        const e = entry as Record<string, unknown>
        const toolStr = typeof e.tool === 'string' ? e.tool : ''
        if (!toolStr) return []
        const dotIdx = toolStr.indexOf('.')
        const serverPart = dotIdx >= 0 ? toolStr.slice(0, dotIdx) : toolStr
        const toolPart = dotIdx >= 0 ? toolStr.slice(dotIdx + 1) : ''
        return [{
          toolId: toolStr,
          serverId: serverPart,
          serverName: serverPart,
          name: toolPart,
          description: '',
          role: 'sensor' as const,
          approvalRequired: false,
        }]
      })
    : []

  const actuatorTools = Array.isArray(capsRaw.actuators)
    ? capsRaw.actuators.flatMap((entry: unknown) => {
        if (!entry || typeof entry !== 'object' || Array.isArray(entry)) return []
        const e = entry as Record<string, unknown>
        const toolStr = typeof e.tool === 'string' ? e.tool : ''
        if (!toolStr) return []
        const dotIdx = toolStr.indexOf('.')
        const serverPart = dotIdx >= 0 ? toolStr.slice(0, dotIdx) : toolStr
        const toolPart = dotIdx >= 0 ? toolStr.slice(dotIdx + 1) : ''
        const approvalRequired = e.approval === 'required'

        // Stash timeout/on_timeout for round-trip passthrough
        const extras: { timeout?: number; on_timeout?: string } = {}
        if (typeof e.timeout === 'number') extras.timeout = e.timeout
        if (typeof e.on_timeout === 'string') extras.on_timeout = e.on_timeout
        if (Object.keys(extras).length > 0) {
          actuatorExtras[toolStr] = extras
        }

        return [{
          toolId: toolStr,
          serverId: serverPart,
          serverName: serverPart,
          name: toolPart,
          description: '',
          role: 'actuator' as const,
          approvalRequired,
        }]
      })
    : []

  const capabilities: CapabilitiesFormState = {
    tools: [...sensorTools, ...actuatorTools],
  }

  // Agent block
  const agentRaw = p.agent && typeof p.agent === 'object' && !Array.isArray(p.agent)
    ? (p.agent as Record<string, unknown>)
    : {}

  const limitsRaw = agentRaw.limits && typeof agentRaw.limits === 'object' && !Array.isArray(agentRaw.limits)
    ? (agentRaw.limits as Record<string, unknown>)
    : {}

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
  }

  const validModels: ModelValue[] = ['claude-opus-4-6', 'claude-sonnet-4-6', 'claude-haiku-4-5-20251001']
  const modelRaw = agentRaw.model
  const model: ModelFormState = {
    model: validModels.includes(modelRaw as ModelValue)
      ? (modelRaw as ModelValue)
      : 'claude-sonnet-4-6',
  }

  const _preamble = typeof agentRaw.preamble === 'string' ? agentRaw.preamble : undefined
  const _feedbackCapabilities = Array.isArray(capsRaw.feedback) ? capsRaw.feedback : undefined

  return {
    identity,
    trigger,
    capabilities,
    task,
    limits,
    concurrency,
    model,
    _preamble,
    _actuatorExtras: Object.keys(actuatorExtras).length > 0 ? actuatorExtras : undefined,
    _feedbackCapabilities,
  }
}

// formStateToYaml serializes FormState back to a YAML string.
export function formStateToYaml(state: FormState): string {
  const { identity, trigger, capabilities, task, limits, concurrency, model } = state

  // Build trigger object
  let triggerObj: Record<string, unknown>
  if (trigger.type === 'cron') {
    triggerObj = { type: 'cron', schedule: trigger.schedule }
  } else if (trigger.type === 'manual') {
    triggerObj = { type: 'manual' }
  } else if (trigger.type === 'scheduled') {
    triggerObj = { type: 'scheduled', fire_at: trigger.fireAt }
  } else if (trigger.type === 'poll') {
    // Parse headers textarea back to object
    const headersObj: Record<string, string> = {}
    if (trigger.request.headers) {
      for (const line of trigger.request.headers.split('\n')) {
        const trimmed = line.trim()
        if (!trimmed) continue
        const sepIdx = trimmed.indexOf(': ')
        if (sepIdx < 0) continue
        headersObj[trimmed.slice(0, sepIdx)] = trimmed.slice(sepIdx + 2)
      }
    }

    const reqObj: Record<string, unknown> = {
      url: trigger.request.url,
      method: trigger.request.method,
    }
    if (Object.keys(headersObj).length > 0) reqObj.headers = headersObj
    if (trigger.request.body) reqObj.body = trigger.request.body

    triggerObj = {
      type: 'poll',
      interval: trigger.interval,
      request: reqObj,
      filter: trigger.filter,
    }
  } else {
    triggerObj = { type: 'webhook' }
  }

  // Build capabilities
  const sensors = capabilities.tools
    .filter(t => t.role === 'sensor')
    .map(t => ({ tool: `${t.serverName}.${t.name}` }))

  const actuators = capabilities.tools
    .filter(t => t.role === 'actuator')
    .map(t => {
      const toolId = t.toolId
      const extras = state._actuatorExtras?.[toolId] ?? {}
      const entry: Record<string, unknown> = { tool: `${t.serverName}.${t.name}` }
      if (extras.timeout !== undefined) entry.timeout = extras.timeout
      if (extras.on_timeout !== undefined) entry.on_timeout = extras.on_timeout
      if (t.approvalRequired) entry.approval = 'required'
      return entry
    })

  const capsObj: Record<string, unknown> = { sensors, actuators }
  if (state._feedbackCapabilities !== undefined) {
    capsObj.feedback = state._feedbackCapabilities
  }

  // Build agent block
  const agentObj: Record<string, unknown> = {}
  if (state._preamble !== undefined) agentObj.preamble = state._preamble
  agentObj.model = model.model
  agentObj.task = task.task
  agentObj.limits = {
    max_tokens_per_run: limits.max_tokens_per_run,
    max_tool_calls_per_run: limits.max_tool_calls_per_run,
  }
  agentObj.concurrency = concurrency.concurrency

  const doc: Record<string, unknown> = {
    name: identity.name,
  }
  if (identity.description) doc.description = identity.description
  if (identity.folder) doc.folder = identity.folder
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
