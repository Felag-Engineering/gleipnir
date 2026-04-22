import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { EditorTopBar } from '@/components/AgentEditor/EditorTopBar/EditorTopBar'
import { DeleteAgentModal } from '@/components/AgentEditor/DeleteAgentModal'
import { TriggerRunModal } from '@/components/TriggerRunModal/TriggerRunModal'
import { PolicyIdentitySection } from '@/components/AgentEditor/FormMode/PolicyIdentitySection'
import { TriggerSection } from '@/components/AgentEditor/FormMode/TriggerSection'
import { CapabilitiesSection } from '@/components/AgentEditor/FormMode/CapabilitiesSection'
import { TaskInstructionsSection } from '@/components/AgentEditor/FormMode/TaskInstructionsSection'
import { RunLimitsSection } from '@/components/AgentEditor/FormMode/RunLimitsSection'
import { ConcurrencySection } from '@/components/AgentEditor/FormMode/ConcurrencySection'
import { ModelSection } from '@/components/AgentEditor/FormMode/ModelSection'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import { ErrorBanner } from '@/components/form/ErrorBanner'
import { usePolicy, usePolicies } from '@/hooks/queries/policies'
import { useSavePolicy, useDeletePolicy, usePausePolicy, useResumePolicy } from '@/hooks/mutations/policies'
import { ApiError } from '@/api/fetch'
import { usePageTitle } from '@/hooks/usePageTitle'
import NotFoundPage from '@/pages/NotFoundPage'
import { defaultFormState, FormState, formStateToYaml, yamlToFormState } from '@/components/AgentEditor/agentEditorUtils'
import { validateFormState, type FormIssue } from '@/components/AgentEditor/validateFormState'
import styles from './AgentEditorPage.module.css'

// splitIssuesBySection partitions a flat FormIssue list into buckets by the
// canonical field prefix. Each section receives only the issues that belong to
// it so it can render inline FieldError components without global state.
function splitIssuesBySection(issues: FormIssue[]) {
  return {
    identity: issues.filter(iss => iss.field === 'name'),
    trigger: issues.filter(iss => iss.field.startsWith('trigger.')),
    capabilities: issues.filter(iss => iss.field.startsWith('capabilities')),
    task: issues.filter(iss => iss.field === 'agent.task'),
    model: issues.filter(iss => iss.field.startsWith('model.')),
    limits: issues.filter(iss => iss.field.startsWith('agent.limits.')),
    // Explicit field names rather than startsWith('agent.') because agent.task
    // and agent.limits.* belong to separate sections.
    concurrency: issues.filter(iss => iss.field === 'agent.concurrency' || iss.field === 'agent.queue_depth'),
  }
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
  const [formState, setFormState] = useState<FormState>(() => defaultFormState())
  // issues holds the current set of validation errors (either from client-side
  // validation or from the server). detailMsg holds a non-structured error
  // message (e.g. "already exists") when the server does not return issues[].
  const [issues, setIssues] = useState<FormIssue[]>([])
  const [detailMsg, setDetailMsg] = useState<string | null>(null)
  const [savedPolicyId, setSavedPolicyId] = useState<string | undefined>(id)
  const [deleteModalOpen, setDeleteModalOpen] = useState(false)
  const [deleteError, setDeleteError] = useState<ApiError | null>(null)
  const [triggerModalOpen, setTriggerModalOpen] = useState(false)

  // Initialize from fetched policy data
  useEffect(() => {
    if (!id) {
      setFormState(defaultFormState())
      setIsDirty(false)
      return
    }
    if (policy) {
      const parsed = yamlToFormState(policy.yaml)
      if (parsed) setFormState(parsed)
      setIsDirty(false)
    }
  }, [id, policy])

  function handleFormChange(patch: Partial<FormState>) {
    setFormState(prev => ({ ...prev, ...patch }))
    setIsDirty(true)
  }

  async function handleSave() {
    // Client-side validation runs first. If it finds issues, short-circuit:
    // display inline errors + banner and scroll to the first offending field.
    const clientIssues = validateFormState(formState)
    if (clientIssues.length > 0) {
      setIssues(clientIssues)
      setDetailMsg(null)
      scrollToField(clientIssues[0].field)
      return
    }

    setIssues([])
    setDetailMsg(null)

    const yaml = formStateToYaml(formState)
    try {
      const result = await savePolicy.mutateAsync({ id, yaml })
      setIsDirty(false)
      setSavedPolicyId(result.id)
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
        scrollToField(serverIssues[0].field)
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
      navigate('/agents')
    } catch (e) {
      setDeleteError(e as ApiError)
    }
  }

  async function handlePause() {
    if (!id) return
    try { await pausePolicy.mutateAsync(id) } catch { /* error surface handled by TanStack Query */ }
  }

  async function handleResume() {
    if (!id) return
    try { await resumePolicy.mutateAsync(id) } catch { /* error surface handled by TanStack Query */ }
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
          <ErrorBanner
            issues={bannerIssues}
            onDismiss={() => { setIssues([]); setDetailMsg(null) }}
            onIssueClick={scrollToField}
          />
          <div className={styles.formPane}>
            <PolicyIdentitySection
              value={formState.identity}
              onChange={v => handleFormChange({ identity: v })}
              existingFolders={existingFolders}
              errors={sectionIssues.identity}
            />
            <TriggerSection
              value={formState.trigger}
              onChange={v => handleFormChange({ trigger: v })}
              policyId={savedPolicyId}
              errors={sectionIssues.trigger}
            />
            <CapabilitiesSection
              value={formState.capabilities}
              onChange={v => handleFormChange({ capabilities: v })}
              errors={sectionIssues.capabilities}
            />
            <TaskInstructionsSection
              value={formState.task}
              onChange={v => handleFormChange({ task: v })}
              errors={sectionIssues.task}
            />
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
        </div>
      </ErrorBoundary>
    </div>
  )
}

export default AgentEditorPage
