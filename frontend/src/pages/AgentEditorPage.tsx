import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { EditorTopBar } from '@/components/AgentEditor/EditorTopBar/EditorTopBar'
import { DeleteAgentModal } from '@/components/AgentEditor/DeleteAgentModal'
import { TriggerRunModal } from '@/components/TriggerRunModal/TriggerRunModal'
import { PolicyIdentitySection } from '@/components/AgentEditor/FormMode/PolicyIdentitySection'
import { TriggerSection } from '@/components/AgentEditor/FormMode/TriggerSection'
import { CapabilitiesSection } from '@/components/AgentEditor/FormMode/CapabilitiesSection'
import { AudienceSection } from '@/components/AgentEditor/FormMode/AudienceSection'
import { TaskInstructionsSection } from '@/components/AgentEditor/FormMode/TaskInstructionsSection'
import { RunLimitsSection } from '@/components/AgentEditor/FormMode/RunLimitsSection'
import { ConcurrencySection } from '@/components/AgentEditor/FormMode/ConcurrencySection'
import { ModelSection } from '@/components/AgentEditor/FormMode/ModelSection'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import { ErrorBanner } from '@/components/form/ErrorBanner'
import { Tabs, tabId, panelId, type TabDescriptor } from '@/components/Tabs'
import { Button } from '@/components/Button'
import { useToast } from '@/components/Toast'
import { usePolicy, usePolicies } from '@/hooks/queries/policies'
import { useSavePolicy, useDeletePolicy, usePausePolicy, useResumePolicy } from '@/hooks/mutations/policies'
import { ApiError } from '@/api/fetch'
import type { ApiMcpTool } from '@/api/types'
import { queryKeys } from '@/hooks/queryKeys'
import { usePageTitle } from '@/hooks/usePageTitle'
import NotFoundPage from '@/pages/NotFoundPage'
import { defaultFormState, FormState, formStateToYaml, yamlToFormState } from '@/components/AgentEditor/agentEditorUtils'
import { validateFormState, type FormIssue } from '@/components/AgentEditor/validateFormState'
import styles from './AgentEditorPage.module.css'
import alerts from '@/styles/alerts.module.css'

// Draft state key for the create-new-agent flow. Scoped to the /agents/new
// route only — the edit route always seeds from server data, which avoids
// stale-draft-vs-fresh-server races. See plan §6 for the design rationale.
const DRAFT_KEY_NEW = 'policyDraft:new'

function readDraft(): FormState | null {
  try {
    const raw = localStorage.getItem(DRAFT_KEY_NEW)
    if (!raw) return null
    const parsed = JSON.parse(raw) as FormState
    // Defend against drafts saved before the audience field existed (when
    // JSON.parse produces `audience: undefined`). Normalize to empty string.
    if (parsed.audience == null) parsed.audience = { name: '' }
    return parsed
  } catch {
    return null
  }
}

function writeDraft(next: FormState) {
  try {
    localStorage.setItem(DRAFT_KEY_NEW, JSON.stringify(next))
  } catch {
    // quota / private-mode — silently ignored, draft is best-effort
  }
}

// findDisabledGrantNames returns display names ("serverName.toolName") for any
// tools in formState.capabilities.tools that are currently disabled according
// to the TanStack Query cache. It checks every server's toolsAll cache entry.
//
// Cache-cold guard: if getQueryData returns undefined for a server (cache not
// yet populated), that server is skipped and contributes no names. This avoids
// false-positive banners when the cache hasn't loaded yet — the runtime check
// at internal/mcp/registry.go is the authoritative enforcement gate.
function findDisabledGrantNames(formState: FormState, queryClient: ReturnType<typeof useQueryClient>): string[] {
  // Collect all known disabled tool identifiers from every populated server cache.
  // Each disabled tool is indexed by both its UUID (used when added via the picker)
  // and its "serverName.toolName" composite (used when loaded from existing YAML).
  const disabledIds = new Map<string, string>() // identifier → display name

  const servers = queryClient.getQueryData<Array<{ id: string; name: string }>>(queryKeys.servers.all) ?? []
  for (const server of servers) {
    const tools = queryClient.getQueryData<ApiMcpTool[]>(queryKeys.servers.toolsAll(server.id))
    if (!tools) continue // cache cold for this server — fail open, skip
    for (const tool of tools) {
      if (!tool.enabled) {
        const displayName = `${server.name}.${tool.name}`
        disabledIds.set(tool.id, displayName)
        disabledIds.set(displayName, displayName)
      }
    }
  }

  if (disabledIds.size === 0) return []

  return formState.capabilities.tools
    .filter(t => disabledIds.has(t.toolId) || disabledIds.has(`${t.serverName}.${t.name}`))
    .map(t => {
      // Prefer the canonical display name from the cache; fall back to form state.
      return disabledIds.get(t.toolId) ?? disabledIds.get(`${t.serverName}.${t.name}`) ?? `${t.serverName}.${t.name}`
    })
}

// splitIssuesBySection partitions a flat FormIssue list into buckets by the
// canonical field prefix. Each section receives only the issues that belong to
// it so it can render inline FieldError components without global state.
function splitIssuesBySection(issues: FormIssue[]) {
  return {
    identity: issues.filter(iss => iss.field === 'name'),
    trigger: issues.filter(iss => iss.field.startsWith('trigger.')),
    capabilities: issues.filter(iss => iss.field.startsWith('capabilities')),
    // audience bucket is forward-compatible — no backend validation yet in v1
    audience: issues.filter(iss => iss.field === 'audience'),
    task: issues.filter(iss => iss.field === 'agent.task'),
    model: issues.filter(iss => iss.field.startsWith('model.')),
    limits: issues.filter(iss => iss.field.startsWith('agent.limits.')),
    // Explicit field names rather than startsWith('agent.') because agent.task
    // and agent.limits.* belong to separate sections.
    concurrency: issues.filter(iss => iss.field === 'agent.concurrency' || iss.field === 'agent.queue_depth'),
  }
}

// The four authoring tabs, in display order. Each groups one or more of the
// eight form sections so operators focus on one concern at a time (#711):
//   basics       — PolicyIdentity + TaskInstructions
//   trigger      — Trigger
//   capabilities — Capabilities + Audience
//   modelLimits  — Model + RunLimits + Concurrency
type TabKey = 'basics' | 'trigger' | 'capabilities' | 'modelLimits'

const TAB_PREFIX = 'agent-editor'

const TAB_ORDER: TabKey[] = ['basics', 'trigger', 'capabilities', 'modelLimits']

const TAB_LABELS: Record<TabKey, string> = {
  basics: 'Basics',
  trigger: 'Trigger',
  capabilities: 'Capabilities',
  modelLimits: 'Model & Limits',
}

// One-line orientation for each tab, shown under the panel title. Written from
// the operator's side of the screen — what they set here, in plain terms.
const TAB_LEAD: Record<TabKey, string> = {
  basics: 'Name your agent and describe the job it does.',
  trigger: 'Choose what starts a run, and how often.',
  capabilities: 'Grant the tools and channels this agent may use.',
  modelLimits: 'Pick the model and set guardrails for each run.',
}

// PanelHeader gives every tab a readable title + lead-in, establishing the
// top-level hierarchy the faint section eyebrows alone couldn't carry.
function PanelHeader({ tab }: { tab: TabKey }) {
  return (
    <header className={styles.panelHeader}>
      <h2 className={styles.panelTitle}>{TAB_LABELS[tab]}</h2>
      <p className={styles.panelLead}>{TAB_LEAD[tab]}</p>
    </header>
  )
}

// tabForField maps a validation issue's field path to the tab that owns the
// section rendering it. Mirrors splitIssuesBySection's bucket logic so a
// deep-linked error opens the right tab.
function tabForField(field: string): TabKey {
  if (field.startsWith('trigger.')) return 'trigger'
  if (field.startsWith('capabilities') || field === 'audience') return 'capabilities'
  if (
    field.startsWith('model.') ||
    field.startsWith('agent.limits.') ||
    field === 'agent.concurrency' ||
    field === 'agent.queue_depth'
  ) {
    return 'modelLimits'
  }
  // name, agent.task, and anything unrecognized land on Basics.
  return 'basics'
}

// scrollToField scrolls the first element with data-field="<field>" into view
// and focuses the first focusable child inside it.
function scrollToField(field: string) {
  const el = document.querySelector<HTMLElement>(`[data-field="${CSS.escape(field)}"]`)
  if (!el) return
  // scrollIntoView is not available in all environments (e.g. jsdom in tests).
  el.scrollIntoView?.({ block: 'center', behavior: 'smooth' })
  const focusable = el.querySelector<HTMLElement>('input, textarea, select, button')
  focusable?.focus({ preventScroll: true })
}

export function AgentEditorPage() {
  const { id } = useParams<{ id?: string }>()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const toast = useToast()

  const { data: policy, status: policyStatus, error: policyErrorObj } = usePolicy(id)
  const { data: allPolicies } = usePolicies()
  const savePolicy = useSavePolicy()
  const deletePolicy = useDeletePolicy()
  const pausePolicy = usePausePolicy()
  const resumePolicy = useResumePolicy()

  const existingFolders: string[] = allPolicies
    ? [...new Set(allPolicies.map((p) => p.folder).filter((f): f is string => Boolean(f)))]
    : []

  const [isDirty, setIsDirty] = useState(false)

  // Draft preservation for the /agents/new route: read synchronously on first
  // mount so the lazy initializer and the init effect cannot race.
  const firstMount = useRef(true)
  const initialDraft = useRef<FormState | null>(
    !id ? readDraft() : null
  )
  const [formState, setFormState] = useState<FormState>(() =>
    !id && initialDraft.current ? initialDraft.current : defaultFormState()
  )
  // issues holds the current set of validation errors (either from client-side
  // validation or from the server). detailMsg holds a non-structured error
  // message (e.g. "already exists") when the server does not return issues[].
  const [issues, setIssues] = useState<FormIssue[]>([])
  const [detailMsg, setDetailMsg] = useState<string | null>(null)
  const [savedPolicyId, setSavedPolicyId] = useState<string | undefined>(id)
  const [deleteModalOpen, setDeleteModalOpen] = useState(false)
  const [deleteError, setDeleteError] = useState<ApiError | null>(null)
  const [triggerModalOpen, setTriggerModalOpen] = useState(false)
  // savedDisabledTools holds display names of tools that were still disabled
  // after a successful save. Shown as a dismissible warning banner.
  const [savedDisabledTools, setSavedDisabledTools] = useState<string[]>([])
  // activeTab is remembered across re-renders (component state) but not across
  // reloads — a fresh mount always starts on Basics.
  const [activeTab, setActiveTab] = useState<TabKey>('basics')
  // visitedTabs gates the per-tab completion check: a tab shows ✓ only once the
  // operator has actually moved through it (and it satisfies its required
  // fields). A fresh create form therefore starts fully unchecked and fills in
  // as they progress; an existing agent (edit mode) is treated as already
  // visited so its complete sections read as done on load.
  const [visitedTabs, setVisitedTabs] = useState<Set<TabKey>>(
    () => new Set<TabKey>(id ? TAB_ORDER : ['basics']),
  )
  // pendingFocusField defers a scroll/focus until after a tab switch renders —
  // a hidden panel can't receive focus, so we switch the tab first (see the
  // effect below) and focus once the owning panel is visible.
  const [pendingFocusField, setPendingFocusField] = useState<string | null>(null)

  // focusField opens the tab owning `field`, then focuses it after the panel
  // becomes visible. Used by both the ErrorBanner deep-link and post-save
  // scroll-to-first-error.
  function focusField(field: string) {
    setActiveTab(tabForField(field))
    setPendingFocusField(field)
  }

  useEffect(() => {
    if (!pendingFocusField) return
    scrollToField(pendingFocusField)
    setPendingFocusField(null)
  }, [pendingFocusField])

  // Mark the active tab visited. Runs for every path that changes activeTab —
  // tab clicks, keyboard roving, and the error deep-link — so "visited" tracks
  // wherever the operator has actually landed.
  useEffect(() => {
    setVisitedTabs(prev => (prev.has(activeTab) ? prev : new Set(prev).add(activeTab)))
  }, [activeTab])

  // Initialize from fetched policy data. The firstMount ref prevents this
  // effect from clobbering a restored draft on the /agents/new route.
  useEffect(() => {
    if (!id) {
      if (firstMount.current) {
        firstMount.current = false
        if (!initialDraft.current) {
          setFormState(defaultFormState())
          setIsDirty(false)
        } else {
          // Draft restored — mark dirty so unsaved-changes affordance shows.
          setIsDirty(true)
        }
      }
      return
    }
    if (policy) {
      const parsed = yamlToFormState(policy.yaml)
      if (parsed) setFormState(parsed)
      setIsDirty(false)
      // Clear firstMount here too so a later id-less mount in the same session
      // doesn't read a stale true value and skip the draft-restore branch.
      firstMount.current = false
    }
  }, [id, policy])

  function handleFormChange(patch: Partial<FormState>) {
    setFormState(prev => {
      const next = { ...prev, ...patch }
      // Persist draft on every change for the create route only.
      // The edit route always seeds from server data on mount.
      if (!id) writeDraft(next)
      return next
    })
    setIsDirty(true)
    setSavedDisabledTools([])
  }

  async function handleSave() {
    // Client-side validation runs first. If it finds issues, short-circuit:
    // display inline errors + banner and scroll to the first offending field.
    const clientIssues = validateFormState(formState)
    if (clientIssues.length > 0) {
      setIssues(clientIssues)
      setDetailMsg(null)
      focusField(clientIssues[0].field)
      return
    }

    setIssues([])
    setDetailMsg(null)

    const yaml = formStateToYaml(formState)
    try {
      const result = await savePolicy.mutateAsync({ id, yaml })
      setIsDirty(false)
      setSavedPolicyId(result.id)
      setSavedDisabledTools(findDisabledGrantNames(formState, queryClient))
      // Clear the draft on successful save — no longer needed.
      localStorage.removeItem(DRAFT_KEY_NEW)
      toast.success('Agent saved')
      if (!id) {
        navigate(`/agents/${result.id}`, { replace: true })
      }
    } catch (e) {
      const err = e as ApiError
      if (err?.issues?.length) {
        // Server returned structured issues — render them like client-side issues.
        const serverIssues: FormIssue[] = err.issues.map(iss => ({
          field: iss.field ?? '',
          message: iss.message,
        }))
        setIssues(serverIssues)
        setDetailMsg(null)
        focusField(serverIssues[0].field)
      } else {
        // Legacy or non-validation error — fall back to the single-bullet banner.
        setDetailMsg(err?.detail ?? err?.message ?? 'Save failed. Please try again.')
        setIssues([])
      }
    }
  }

  async function handleDelete() {
    if (!id) return
    setDeleteError(null)
    try {
      await deletePolicy.mutateAsync(id)
      setDeleteModalOpen(false)
      toast.success('Agent deleted')
      navigate('/agents')
    } catch (e) {
      setDeleteError(e as ApiError)
    }
  }

  async function handlePause() {
    if (!id) return
    try {
      // No success toast — the status badge flips to Paused, which is the confirmation.
      await pausePolicy.mutateAsync(id)
    } catch {
      toast.error("Couldn't pause agent")
    }
  }

  async function handleResume() {
    if (!id) return
    try {
      // No success toast — the status badge flips back to Active, which is the confirmation.
      await resumePolicy.mutateAsync(id)
    } catch {
      toast.error("Couldn't resume agent")
    }
  }

  // Stable ref so the keydown listener always calls the current handleSave
  // without needing to re-register on every render.
  const handleSaveRef = useRef(handleSave)
  handleSaveRef.current = handleSave

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key === 's') {
        e.preventDefault()
        handleSaveRef.current()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [])

  const canSave = isDirty && !savePolicy.isPending

  const policyName = formState.identity.name || (id ? id : 'New Agent')

  const pageTitle = (id && policyStatus === 'error') ? 'Agent not found' : policyName
  usePageTitle(pageTitle)

  // Show loading/error states only when fetching an existing policy
  if (id && policyStatus === 'pending') {
    return (
      <div className={styles.page}>
        <div className={styles.loadingState}>Loading agent…</div>
      </div>
    )
  }

  if (id && policyStatus === 'error') {
    const is404 = policyErrorObj instanceof ApiError && policyErrorObj.status === 404
    if (is404) {
      return (
        <div className={styles.page}>
          <NotFoundPage
            embedded
            title="Agent not found"
            message={`No agent with ID ${id}. It may have been deleted.`}
            primary={{ label: 'Go to Agents', to: '/agents' }}
            secondary={{ label: 'Go to Dashboard', to: '/dashboard' }}
          />
        </div>
      )
    }
    // Non-404 errors keep the original plain text error state
    return (
      <div className={styles.page}>
        <div className={styles.errorState}>Failed to load agent.</div>
      </div>
    )
  }

  const sectionIssues = splitIssuesBySection(issues)

  // Per-tab error counts drive the badge on each tab. Sums each tab's owned
  // section buckets so a non-active tab surfaces which sections block Save.
  const tabErrorCounts: Record<TabKey, number> = {
    basics: sectionIssues.identity.length + sectionIssues.task.length,
    trigger: sectionIssues.trigger.length,
    capabilities: sectionIssues.capabilities.length + sectionIssues.audience.length,
    modelLimits: sectionIssues.model.length + sectionIssues.limits.length + sectionIssues.concurrency.length,
  }

  // Completion is computed LIVE (not from the post-Save `issues` state) so the
  // check appears the moment a visited tab's required fields are satisfied — a
  // positive signal that encourages, unlike the red badges which stay gated to
  // a Save attempt so we never scold before the operator has tried. A tab is
  // "complete" only when it has been VISITED and contributes no validation
  // issue (i.e. is not blocking Save); the visited gate keeps a fresh form
  // fully unchecked and lets the checks fill in as the operator moves through.
  const liveSection = splitIssuesBySection(validateFormState(formState))
  const tabComplete: Record<TabKey, boolean> = {
    basics: visitedTabs.has('basics') && liveSection.identity.length === 0 && liveSection.task.length === 0,
    trigger: visitedTabs.has('trigger') && liveSection.trigger.length === 0,
    capabilities:
      visitedTabs.has('capabilities') &&
      liveSection.capabilities.length === 0 &&
      liveSection.audience.length === 0,
    modelLimits:
      visitedTabs.has('modelLimits') &&
      liveSection.model.length === 0 &&
      liveSection.limits.length === 0 &&
      liveSection.concurrency.length === 0,
  }

  const tabDescriptors: TabDescriptor[] = TAB_ORDER.map(key => ({
    id: key,
    label: TAB_LABELS[key],
    errorCount: tabErrorCounts[key],
    complete: tabComplete[key],
  }))

  // Prev/next drive the guided Back/Next footer. On the last tab the Next slot
  // becomes the primary Save so the create flow has a clear finish; the top-bar
  // Save stays available throughout for editing.
  const activeIndex = TAB_ORDER.indexOf(activeTab)
  const prevTab = activeIndex > 0 ? TAB_ORDER[activeIndex - 1] : null
  const nextTab = activeIndex < TAB_ORDER.length - 1 ? TAB_ORDER[activeIndex + 1] : null

  // Build the banner issue list: if we have structured issues use them;
  // otherwise fall back to the single detail message.
  const bannerIssues = issues.length > 0
    ? issues
    : detailMsg
      ? [{ message: detailMsg }]
      : []

  return (
    <div className={styles.page}>
      <EditorTopBar
        policyName={policyName}
        canSave={canSave}
        isEditMode={Boolean(id)}
        pausedAt={policy?.paused_at}
        isPauseResumeLoading={pausePolicy.isPending || resumePolicy.isPending}
        isSaving={savePolicy.isPending}
        isDeleting={deletePolicy.isPending}
        onSave={handleSave}
        onDeleteClick={() => setDeleteModalOpen(true)}
        onRunNowClick={id ? () => setTriggerModalOpen(true) : undefined}
        onPauseClick={id ? handlePause : undefined}
        onResumeClick={id ? handleResume : undefined}
      />
      {deleteModalOpen && id && (
        <DeleteAgentModal
          policyId={id}
          policyName={policyName}
          onClose={() => { setDeleteModalOpen(false); setDeleteError(null) }}
          onConfirm={handleDelete}
          isPending={deletePolicy.isPending}
          error={deleteError}
        />
      )}
      {triggerModalOpen && id && (
        <TriggerRunModal
          policyId={id}
          policyName={policyName}
          onClose={() => setTriggerModalOpen(false)}
          onSuccess={(runId) => {
            setTriggerModalOpen(false)
            navigate(`/runs/${runId}`)
          }}
        />
      )}
      <ErrorBoundary>
        <div className={styles.content}>
          {savedDisabledTools.length > 0 && (
            <div className={`${alerts.alertWarning} ${styles.disabledToolBanner}`}>
              {`Policy saved. Note: tool${savedDisabledTools.length > 1 ? 's' : ''} "${savedDisabledTools.join('", "')}" ${savedDisabledTools.length > 1 ? 'are' : 'is'} currently disabled and will block runs.`}
              <button
                className={styles.dismiss}
                onClick={() => setSavedDisabledTools([])}
                aria-label="Dismiss warning"
              >
                ×
              </button>
            </div>
          )}
          <ErrorBanner
            issues={bannerIssues}
            onDismiss={() => { setIssues([]); setDetailMsg(null) }}
            onIssueClick={focusField}
          />
          <div className={styles.formPane}>
            <Tabs
              tabs={tabDescriptors}
              activeId={activeTab}
              onChange={id => setActiveTab(id as TabKey)}
              ariaLabel="Agent configuration"
              idPrefix={TAB_PREFIX}
            />

            <div
              role="tabpanel"
              id={panelId(TAB_PREFIX, 'basics')}
              aria-labelledby={tabId(TAB_PREFIX, 'basics')}
              hidden={activeTab !== 'basics'}
              className={styles.panel}
            >
              <PanelHeader tab="basics" />
              <PolicyIdentitySection
                value={formState.identity}
                onChange={v => handleFormChange({ identity: v })}
                existingFolders={existingFolders}
                errors={sectionIssues.identity}
              />
              <TaskInstructionsSection
                value={formState.task}
                onChange={v => handleFormChange({ task: v })}
                errors={sectionIssues.task}
              />
            </div>

            <div
              role="tabpanel"
              id={panelId(TAB_PREFIX, 'trigger')}
              aria-labelledby={tabId(TAB_PREFIX, 'trigger')}
              hidden={activeTab !== 'trigger'}
              className={styles.panel}
            >
              <PanelHeader tab="trigger" />
              <TriggerSection
                value={formState.trigger}
                onChange={v => handleFormChange({ trigger: v })}
                policyId={savedPolicyId}
                errors={sectionIssues.trigger}
              />
            </div>

            <div
              role="tabpanel"
              id={panelId(TAB_PREFIX, 'capabilities')}
              aria-labelledby={tabId(TAB_PREFIX, 'capabilities')}
              hidden={activeTab !== 'capabilities'}
              className={styles.panel}
            >
              <PanelHeader tab="capabilities" />
              <CapabilitiesSection
                value={formState.capabilities}
                onChange={v => handleFormChange({ capabilities: v })}
                errors={sectionIssues.capabilities}
              />
              <AudienceSection
                value={formState.audience}
                onChange={v => handleFormChange({ audience: v })}
                onNewAudienceClick={() => navigate('/admin/audiences/new')}
                errors={sectionIssues.audience}
              />
            </div>

            <div
              role="tabpanel"
              id={panelId(TAB_PREFIX, 'modelLimits')}
              aria-labelledby={tabId(TAB_PREFIX, 'modelLimits')}
              hidden={activeTab !== 'modelLimits'}
              className={styles.panel}
            >
              <PanelHeader tab="modelLimits" />
              <ModelSection
                value={formState.model}
                onChange={v => handleFormChange({ model: v })}
                errors={sectionIssues.model}
              />
              <RunLimitsSection
                value={formState.limits}
                onChange={v => handleFormChange({ limits: v })}
                errors={sectionIssues.limits}
              />
              <ConcurrencySection
                value={formState.concurrency}
                onChange={v => handleFormChange({ concurrency: v })}
                errors={sectionIssues.concurrency}
              />
            </div>

            {/* One footer, driven by the active tab: guided Back/Next through the
                sequence, ending in a primary Save on the last tab. */}
            <div className={styles.panelFooter}>
              {prevTab ? (
                <Button type="button" variant="secondary" onClick={() => setActiveTab(prevTab)}>
                  ← {TAB_LABELS[prevTab]}
                </Button>
              ) : (
                <span />
              )}
              {nextTab ? (
                <Button type="button" variant="secondary" onClick={() => setActiveTab(nextTab)}>
                  {TAB_LABELS[nextTab]} →
                </Button>
              ) : (
                <Button
                  type="button"
                  variant="primary"
                  onClick={handleSave}
                  disabled={!canSave}
                  loading={savePolicy.isPending}
                >
                  Save agent
                </Button>
              )}
            </div>
          </div>
        </div>
      </ErrorBoundary>
    </div>
  )
}

export default AgentEditorPage
