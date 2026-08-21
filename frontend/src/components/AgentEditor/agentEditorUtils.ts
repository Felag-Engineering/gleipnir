import { dump, load } from 'js-yaml'
import type {
  AudienceFormState,
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
  audience: AudienceFormState
  task: TaskInstructionsFormState
  limits: RunLimitsFormState
  concurrency: ConcurrencyFormState
  model: ModelFormState
  // source is the parsed policy document this state was read from. It is not
  // edited by any section — it exists so formStateToYaml can carry across the
  // fields the form does not model instead of dropping them (#869).
  source?: Record<string, unknown>
}

// ---------------------------------------------------------------------------
// Round-trip preservation (#869)
// ---------------------------------------------------------------------------
//
// The form models a subset of schemas/policy.yaml. Everything outside that
// subset -- `agent.preamble`, `model.options`, `agent.limits.
// max_elicitations_per_run`, and per-tool ADR-017 `params` -- used to be
// destroyed on save, because formStateToYaml built its document from an empty
// object. ADR-019 makes Form the only editing surface, so a field deleted that
// way had no route back, and for `params` the deletion silently widened the
// schema the agent is given.
//
// The rule: the form is authoritative for the keys it models, including
// removing one the operator cleared. Every other key is carried over from
// `source`. Preserved keys are appended after the modelled ones, so a second
// save of an unchanged policy reproduces the first save's output byte for byte.
//
// Adding a field to a MODELLED_* list means "the form now owns this key" -- do
// that when a section starts editing it, and not before.
const MODELLED_ROOT = ['name', 'description', 'folder', 'audience', 'model', 'trigger', 'capabilities', 'agent']
const MODELLED_MODEL = ['provider', 'name']
// `preamble` is listed as form-owned even though no section edits it, which
// means a save deliberately DROPS it. That is pre-existing behaviour pinned by
// "does not emit preamble" in agentEditorUtils.test.ts and left untouched here.
// Note it disagrees with the rest of the system: internal/policy/
// prompt_generator.go:44 still honours agent.preamble and only falls back to the
// default when it is empty, and schemas/policy.yaml:322 still documents it as an
// operator-editable ADR-012 field. Resolving that contradiction is out of scope
// for #869 — see the open question on that issue. Move `preamble` out of this
// list to start preserving it.
const MODELLED_AGENT = ['task', 'limits', 'concurrency', 'queue_depth', 'preamble']
const MODELLED_LIMITS = ['max_tokens_per_run', 'max_tool_calls_per_run']
const MODELLED_CAPABILITIES = ['tools', 'feedback']
const MODELLED_FEEDBACK = ['enabled', 'timeout', 'on_timeout']
const MODELLED_TOOL = ['tool', 'approval', 'timeout', 'on_timeout']
// Union across every trigger variant. Safe as one list because trigger fields
// are only preserved when the type is unchanged (see formStateToYaml).
const MODELLED_TRIGGER = [
  'type', 'auth', 'fire_at', 'cron_expr', 'source', 'event_kind', 'binding',
  'interval', 'match', 'checks',
]

// withPreserved returns rebuilt extended with every key of original the form
// does not model. A modelled key is never taken from original: if the form
// omitted it, the operator removed it and it stays removed.
function withPreserved(
  rebuilt: Record<string, unknown>,
  original: unknown,
  modelled: string[],
): Record<string, unknown> {
  if (!isRecord(original)) return rebuilt
  const out = { ...rebuilt }
  for (const [key, value] of Object.entries(original)) {
    if (modelled.includes(key)) continue
    // rebuilt only ever holds modelled keys, so this cannot fire today. It is
    // here so that adding an unmodelled key to rebuilt later cannot be
    // silently overwritten by a stale value from source.
    if (key in out) continue
    out[key] = value
  }
  return out
}

export const DEFAULT_YAML = `name: ''
description: ''
trigger:
  type: webhook
  auth: hmac
capabilities:
  tools: []
agent:
  task: ''
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
  } else if (triggerType === 'subscribed') {
    trigger = {
      type: 'subscribed',
      source: typeof triggerRaw.source === 'string' ? triggerRaw.source : '',
      eventKind: typeof triggerRaw.event_kind === 'string' ? triggerRaw.event_kind : '',
      binding: isRecord(triggerRaw.binding) ? triggerRaw.binding : {},
    }
  } else if (triggerType === 'cron') {
    const cronExpr = typeof triggerRaw.cron_expr === 'string' ? triggerRaw.cron_expr : ''
    trigger = { type: 'cron', cronExpr }
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
          // Parse-time default: CapabilitiesSection reconciles plugin vs MCP at
          // render by checking whether serverName matches a known plugin instance.
          source: 'mcp' as const,
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

  // When the model block is absent, leave provider and model empty so the form
  // layer (ModelSection) can apply the system default from /api/v1/config.
  // An explicit { provider: '', model: '' } is the "not yet chosen" state that
  // the backend will reject with "model.provider is required" if saved as-is.
  const model: ModelFormState = modelRaw && typeof modelRaw.provider === 'string' && typeof modelRaw.name === 'string'
    ? { provider: modelRaw.provider, model: modelRaw.name }
    : { provider: '', model: '' }

  // Audience — top-level optional string field, matches the Go scanner in
  // internal/policy/audience_refs.go (`Audience string \`yaml:"audience"\``).
  const audience: AudienceFormState = {
    name: typeof p.audience === 'string' ? p.audience : '',
  }

  return {
    identity,
    trigger,
    capabilities,
    audience,
    task,
    limits,
    concurrency,
    model,
    source: p,
  }
}

// formStateToYaml serializes FormState back to a YAML string.
export function formStateToYaml(state: FormState): string {
  const { identity, trigger, capabilities, audience, task, limits, concurrency, model, source } = state
  const src = isRecord(source) ? source : {}

  // Build trigger object
  let triggerObj: Record<string, unknown>
  if (trigger.type === 'manual') {
    triggerObj = { type: 'manual' }
  } else if (trigger.type === 'scheduled') {
    triggerObj = { type: 'scheduled', fire_at: trigger.fireAt }
  } else if (trigger.type === 'cron') {
    triggerObj = { type: 'cron', cron_expr: trigger.cronExpr }
  } else if (trigger.type === 'subscribed') {
    const bindingObj = trigger.binding && Object.keys(trigger.binding).length > 0
      ? { binding: trigger.binding }
      : {};
    triggerObj = {
      type: 'subscribed',
      source: trigger.source,
      event_kind: trigger.eventKind,
      ...bindingObj,
    };
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

  // Changing the trigger type swaps the whole variant, so the previous
  // variant's fields are not ours to keep. Preserve within the same type only.
  const srcTrigger = isRecord(src.trigger) ? src.trigger : undefined
  if (srcTrigger && srcTrigger.type === triggerObj.type) {
    triggerObj = withPreserved(triggerObj, srcTrigger, MODELLED_TRIGGER)
  }

  // Build capabilities — single tools array.
  //
  // Original entries are indexed by grant string so each rebuilt entry can
  // recover its own unmodelled keys — ADR-017 `params` above all. Queued rather
  // than single-valued so that a policy granting the same tool twice stays
  // deterministic instead of giving both entries the first one's params.
  const srcCaps = isRecord(src.capabilities) ? src.capabilities : {}
  const srcTools = new Map<string, Record<string, unknown>[]>()
  if (Array.isArray(srcCaps.tools)) {
    for (const entry of srcCaps.tools) {
      if (!isRecord(entry) || typeof entry.tool !== 'string') continue
      const queued = srcTools.get(entry.tool)
      if (queued) queued.push(entry)
      else srcTools.set(entry.tool, [entry])
    }
  }

  // t.serverName carries the MCP server display name OR the plugin instance_name;
  // both produce the correct `<source>.<tool>` dot-notation grant string.
  const tools = capabilities.tools.map(t => {
    const grant = `${t.serverName}.${t.name}`
    const entry: Record<string, unknown> = { tool: grant }
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
    return withPreserved(entry, srcTools.get(grant)?.shift(), MODELLED_TOOL)
  })

  let capsObj: Record<string, unknown> = { tools }
  // Emit the feedback block only when enabled — omitting it means disabled,
  // following the same pattern as other optional fields (description, folder).
  if (capabilities.feedback.enabled) {
    const feedbackObj: Record<string, unknown> = { enabled: true }
    if (capabilities.feedback.timeout) feedbackObj.timeout = capabilities.feedback.timeout
    if (capabilities.feedback.onTimeout) feedbackObj.on_timeout = capabilities.feedback.onTimeout
    capsObj.feedback = withPreserved(feedbackObj, srcCaps.feedback, MODELLED_FEEDBACK)
  }
  capsObj = withPreserved(capsObj, srcCaps, MODELLED_CAPABILITIES)

  // Build agent block
  const srcAgent = isRecord(src.agent) ? src.agent : {}
  let agentObj: Record<string, unknown> = {}
  agentObj.task = task.task
  agentObj.limits = withPreserved(
    {
      max_tokens_per_run: limits.max_tokens_per_run,
      max_tool_calls_per_run: limits.max_tool_calls_per_run,
    },
    srcAgent.limits,
    MODELLED_LIMITS,
  )
  agentObj.concurrency = concurrency.concurrency
  // Emit queue_depth only when mode is queue and depth is non-zero.
  // Omitting it lets the backend apply model.DefaultQueueDepth.
  if (concurrency.concurrency === 'queue' && concurrency.queueDepth > 0) {
    agentObj.queue_depth = concurrency.queueDepth
  }
  // Carries `agent.preamble` (ADR-012) across; the form has no section for it.
  agentObj = withPreserved(agentObj, srcAgent, MODELLED_AGENT)

  let doc: Record<string, unknown> = {
    name: identity.name,
  }
  if (identity.description) doc.description = identity.description
  if (identity.folder) doc.folder = identity.folder
  // Emit audience only when set — an empty string means "no audience", which
  // must not be serialized as an empty reference. Placed in the metadata
  // cluster (name / description / folder / audience) before model.
  if (audience.name) doc.audience = audience.name
  // Carries `model.options` (e.g. enable_prompt_caching) across.
  doc.model = withPreserved(
    { provider: model.provider, name: model.model },
    src.model,
    MODELLED_MODEL,
  )
  doc.trigger = triggerObj
  doc.capabilities = capsObj
  doc.agent = agentObj
  doc = withPreserved(doc, src, MODELLED_ROOT)

  return dump(doc, { lineWidth: -1 })
}

// defaultFormState returns a FormState seeded from DEFAULT_YAML.
// DEFAULT_YAML is valid, so yamlToFormState will never return null.
// yamlToFormState already populates audience: { name: '' } when the field
// is absent, so no additional seeding is needed here.
export function defaultFormState(): FormState {
  return yamlToFormState(DEFAULT_YAML) as FormState
}
