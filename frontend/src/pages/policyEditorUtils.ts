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
  _toolExtras?: Record<string, { timeout?: number; on_timeout?: string }>
  _feedbackCapabilities?: unknown[]
}

export const DEFAULT_YAML = `name: new-policy
description: ''
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
  if (triggerType === 'manual') {
    trigger = { type: 'manual' }
  } else if (triggerType === 'scheduled') {
    const fireAtRaw = Array.isArray(triggerRaw.fire_at) ? triggerRaw.fire_at : []
    trigger = {
      type: 'scheduled',
      fireAt: fireAtRaw.filter((v: unknown) => typeof v === 'string') as string[],
    }
  } else {
    trigger = { type: 'webhook' }
  }

  // Capabilities
  const capsRaw = p.capabilities && typeof p.capabilities === 'object' && !Array.isArray(p.capabilities)
    ? (p.capabilities as Record<string, unknown>)
    : {}

  const toolExtras: Record<string, { timeout?: number; on_timeout?: string }> = {}

  const tools = Array.isArray(capsRaw.tools)
    ? capsRaw.tools.flatMap((entry: unknown) => {
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
          toolExtras[toolStr] = extras
        }

        return [{
          toolId: toolStr,
          serverId: serverPart,
          serverName: serverPart,
          name: toolPart,
          description: '',
          role: 'tool' as const,
          approvalRequired,
        }]
      })
    : []

  const capabilities: CapabilitiesFormState = { tools }

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
    _toolExtras: Object.keys(toolExtras).length > 0 ? toolExtras : undefined,
    _feedbackCapabilities,
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
  } else {
    triggerObj = { type: 'webhook' }
  }

  // Build capabilities — single tools array
  const tools = capabilities.tools.map(t => {
    const toolId = t.toolId
    const extras = state._toolExtras?.[toolId] ?? {}
    const entry: Record<string, unknown> = { tool: `${t.serverName}.${t.name}` }
    if (extras.timeout !== undefined) entry.timeout = extras.timeout
    if (extras.on_timeout !== undefined) entry.on_timeout = extras.on_timeout
    if (t.approvalRequired) entry.approval = 'required'
    return entry
  })

  const capsObj: Record<string, unknown> = { tools }
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
