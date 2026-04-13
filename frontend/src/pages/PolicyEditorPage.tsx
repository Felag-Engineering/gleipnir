import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { EditorTopBar } from '@/components/PolicyEditor/EditorTopBar/EditorTopBar'
import { YamlEditor } from '@/components/PolicyEditor/YamlEditor/YamlEditor'
import { PolicyIdentitySection } from '@/components/PolicyEditor/FormMode/PolicyIdentitySection'
import { TriggerSection } from '@/components/PolicyEditor/FormMode/TriggerSection'
import { CapabilitiesSection } from '@/components/PolicyEditor/FormMode/CapabilitiesSection'
import { TaskInstructionsSection } from '@/components/PolicyEditor/FormMode/TaskInstructionsSection'
import { RunLimitsSection } from '@/components/PolicyEditor/FormMode/RunLimitsSection'
import { ConcurrencySection } from '@/components/PolicyEditor/FormMode/ConcurrencySection'
import { ModelSection } from '@/components/PolicyEditor/FormMode/ModelSection'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import { usePolicy } from '@/hooks/queries/policies'
import { useSavePolicy } from '@/hooks/mutations/policies'
import { useDeletePolicy } from '@/hooks/mutations/policies'
import { ApiError } from '@/api/fetch'
import { usePageTitle } from '@/hooks/usePageTitle'
import { DEFAULT_YAML, defaultFormState, FormState, formStateToYaml, yamlToFormState } from '@/components/PolicyEditor/policyEditorUtils'
import styles from './PolicyEditorPage.module.css'

export function PolicyEditorPage() {
  const { id } = useParams<{ id?: string }>()
  const navigate = useNavigate()

  const { data: policy, status: policyStatus } = usePolicy(id)
  const savePolicy = useSavePolicy()
  const deletePolicy = useDeletePolicy()

  const [mode, setMode] = useState<'form' | 'yaml'>('form')
  const [yamlString, setYamlString] = useState(DEFAULT_YAML)
  const [yamlValid, setYamlValid] = useState(true)
  const [isDirty, setIsDirty] = useState(false)
  const [formState, setFormState] = useState<FormState>(() => defaultFormState())
  const [saveError, setSaveError] = useState<string | null>(null)
  const [savedPolicyId, setSavedPolicyId] = useState<string | undefined>(id)

  // Initialize from fetched policy data
  useEffect(() => {
    if (!id) {
      setYamlString(DEFAULT_YAML)
      setFormState(defaultFormState())
      setIsDirty(false)
      return
    }
    if (policy) {
      setYamlString(policy.yaml)
      const parsed = yamlToFormState(policy.yaml)
      if (parsed) setFormState(parsed)
      setIsDirty(false)
    }
  }, [id, policy])

  function handleModeChange(next: 'form' | 'yaml') {
    if (next === mode) return
    if (next === 'yaml') {
      setYamlString(formStateToYaml(formState))
      setMode('yaml')
    } else {
      const parsed = yamlToFormState(yamlString)
      if (parsed === null) {
        setSaveError('Cannot switch to Form mode: YAML is malformed or missing required fields.')
        return
      }
      setFormState(parsed)
      setSaveError(null)
      setMode('form')
    }
  }

  function handleFormChange(patch: Partial<FormState>) {
    setFormState(prev => ({ ...prev, ...patch }))
    setIsDirty(true)
  }

  function handleYamlChange(value: string) {
    setYamlString(value)
    setIsDirty(true)
  }

  async function handleSave() {
    setSaveError(null)
    const yaml = mode === 'form' ? formStateToYaml(formState) : yamlString
    try {
      const result = await savePolicy.mutateAsync({ id, yaml })
      setIsDirty(false)
      setSavedPolicyId(result.id)
      if (!id) {
        navigate(`/agents/${result.id}`, { replace: true })
      }
    } catch (e) {
      const err = e as ApiError
      setSaveError(err?.detail ?? err?.message ?? 'Save failed. Please try again.')
    }
  }

  async function handleDelete() {
    if (!id) return
    try {
      await deletePolicy.mutateAsync(id)
      navigate('/agents')
    } catch (e) {
      const err = e as ApiError
      setSaveError(err?.detail ?? err?.message ?? 'Delete failed. Please try again.')
    }
  }

  // Stable ref so the keydown listener always calls the current handleSave
  // without needing to re-register on every render (same pattern as YamlEditor.tsx)
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

  const canSave = isDirty && (mode === 'form' || yamlValid) && !savePolicy.isPending

  const policyName =
    mode === 'form'
      ? (formState.identity.name || (id ? id : 'New Agent'))
      : (id ? (policy?.name ?? id) : 'New Agent')

  usePageTitle(policyName)

  // Show loading/error states only when fetching an existing policy
  if (id && policyStatus === 'pending') {
    return (
      <div className={styles.page}>
        <div className={styles.loadingState}>Loading agent…</div>
      </div>
    )
  }

  if (id && policyStatus === 'error') {
    return (
      <div className={styles.page}>
        <div className={styles.errorState}>Failed to load agent.</div>
      </div>
    )
  }

  return (
    <div className={styles.page}>
      <EditorTopBar
        policyName={policyName}
        isDirty={isDirty}
        mode={mode}
        canSave={canSave}
        isEditMode={Boolean(id)}
        onModeChange={handleModeChange}
        onSave={handleSave}
        onDelete={handleDelete}
      />
      <ErrorBoundary>
        <div className={styles.content}>
          {saveError && (
            <div className={styles.errorBanner}>
              <span className={styles.errorBannerMessage}>{saveError}</span>
              <button className={styles.errorBannerClose} onClick={() => setSaveError(null)}>×</button>
            </div>
          )}
          {mode === 'yaml' ? (
            <div className={styles.yamlPane}>
              <YamlEditor
                value={yamlString}
                onChange={handleYamlChange}
                onValidityChange={setYamlValid}
              />
            </div>
          ) : (
            <div className={styles.formPane}>
              <PolicyIdentitySection
                value={formState.identity}
                onChange={v => handleFormChange({ identity: v })}
              />
              <TriggerSection
                value={formState.trigger}
                onChange={v => handleFormChange({ trigger: v })}
                policyId={savedPolicyId}
              />
              <CapabilitiesSection
                value={formState.capabilities}
                onChange={v => handleFormChange({ capabilities: v })}
              />
              <TaskInstructionsSection
                value={formState.task}
                onChange={v => handleFormChange({ task: v })}
              />
              <ModelSection
                value={formState.model}
                onChange={v => handleFormChange({ model: v })}
              />
              <RunLimitsSection
                value={formState.limits}
                onChange={v => handleFormChange({ limits: v })}
              />
              <ConcurrencySection
                value={formState.concurrency}
                onChange={v => handleFormChange({ concurrency: v })}
              />
            </div>
          )}
        </div>
      </ErrorBoundary>
    </div>
  )
}

export default PolicyEditorPage
