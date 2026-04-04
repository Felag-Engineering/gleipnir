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
} from '@/components/PolicyEditor/FormMode/types'

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
  // Passthrough fields preserved across round-trips but not editable in form UI
  _preamble?: string
  _toolExtras?: Record<string, { timeout?: number; on_timeout?: string }>
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
  } else {
    trigger = { type: 'webhook' }
  }

  // Capabilities
  const capsRaw = isRecord(p.capabilities) ? p.capabilities : {}

  const toolExtras: Record<string, { timeout?: number; on_timeout?: string }> = {}

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
          role: 'tool' as const, // placeholder — CapabilitiesSection reconciles with registry
          approvalRequired,
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
  }

  // Read model from top-level model: section
  const modelRaw = isRecord(p.model) ? p.model : null

  const model: ModelFormState = modelRaw && typeof modelRaw.provider === 'string' && typeof modelRaw.name === 'string'
    ? { provider: modelRaw.provider, model: modelRaw.name }
    : { provider: 'anthropic', model: 'claude-sonnet-4-6' }

  const _preamble = typeof agentRaw.preamble === 'string' ? agentRaw.preamble : undefined

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
  if (state._preamble !== undefined) agentObj.preamble = state._preamble
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
