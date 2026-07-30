import { useState, useCallback } from 'react'
import { useNavigate } from 'react-router'
import { Button } from '@/components/Button/Button'
import type {
  ApiAudience,
  ApiAudienceEntry,
  ApiAudienceReferences,
  ApiPluginInstanceForAudience,
  AudienceCreateRequest,
  AudienceUpdateRequest,
} from '@/api/types'
import type { ApiError } from '@/api/fetch'
import { RoutingPreview } from './RoutingPreview'
import { EntryRow } from './EntryRow'
import { SaveGuardDialog } from './SaveGuardDialog'
import alertStyles from '@/styles/alerts.module.css'
import styles from './AudienceEditor.module.css'

interface Props {
  initial: ApiAudience | null
  pluginInstances: ApiPluginInstanceForAudience[]
  references: ApiAudienceReferences | null
  canManage: boolean
  onSave: (req: AudienceCreateRequest | AudienceUpdateRequest) => Promise<ApiAudience>
  onDelete?: () => Promise<void>
  saveError: ApiError | null
  deleteError: ApiError | null
}

function makeBlankEntry(): ApiAudienceEntry {
  return {
    id: `new-${Date.now()}-${Math.random()}`,
    plugin_instance_id: '',
    position: 0,
    notify: true,
    request: false,
    config: {},
  }
}

export function AudienceEditor({
  initial,
  pluginInstances,
  references,
  canManage,
  onSave,
  onDelete,
  saveError,
  deleteError,
}: Props) {
  const navigate = useNavigate()

  const [name, setName] = useState(initial?.name ?? '')
  const [disableInAppFallback, setDisableInAppFallback] = useState(
    initial?.disable_in_app_fallback ?? false,
  )
  // Filter out auto entries — they're server-injected; we keep them only for display.
  const [entries, setEntries] = useState<ApiAudienceEntry[]>(
    (initial?.entries ?? []).filter((e) => !e.auto),
  )
  const [dirty, setDirty] = useState(false)
  const [showSaveGuard, setShowSaveGuard] = useState(false)
  const [isSaving, setIsSaving] = useState(false)
  const [isDeleting, setIsDeleting] = useState(false)

  // Drag state: which index is being dragged and which index is the drop target.
  const [dragIndex, setDragIndex] = useState<number | null>(null)
  const [dragOverIndex, setDragOverIndex] = useState<number | null>(null)

  function markDirty() {
    if (!dirty) setDirty(true)
  }

  function handleNameChange(e: React.ChangeEvent<HTMLInputElement>) {
    setName(e.target.value)
    markDirty()
  }

  function handleDisableFallbackChange(e: React.ChangeEvent<HTMLInputElement>) {
    setDisableInAppFallback(e.target.checked)
    markDirty()
  }

  function handleEntryChange(index: number, updated: ApiAudienceEntry) {
    setEntries((prev) => prev.map((e, i) => (i === index ? updated : e)))
    markDirty()
  }

  function handleAddEntry() {
    setEntries((prev) => [...prev, makeBlankEntry()])
    markDirty()
  }

  function handleRemoveEntry(index: number) {
    setEntries((prev) => prev.filter((_, i) => i !== index))
    markDirty()
  }

  function handleMoveUp(index: number) {
    if (index === 0) return
    setEntries((prev) => {
      const next = [...prev]
      ;[next[index - 1], next[index]] = [next[index], next[index - 1]]
      return next
    })
    markDirty()
  }

  function handleMoveDown(index: number) {
    setEntries((prev) => {
      if (index >= prev.length - 1) return prev
      const next = [...prev]
      ;[next[index], next[index + 1]] = [next[index + 1], next[index]]
      return next
    })
    markDirty()
  }

  const handleDragStart = useCallback((index: number) => {
    setDragIndex(index)
  }, [])

  const handleDragOver = useCallback(
    (e: React.DragEvent, index: number) => {
      e.preventDefault()
      // No drop target for the row being dragged itself — dropping onto it is a no-op.
      if (dragIndex === null || index === dragIndex) return
      if (dragOverIndex !== index) setDragOverIndex(index)
    },
    [dragIndex, dragOverIndex],
  )

  // dragEnd fires on the source row whether the drag was dropped or aborted
  // (released outside any row), so it is the guaranteed cleanup for both paths.
  const handleDragEnd = useCallback(() => {
    setDragIndex(null)
    setDragOverIndex(null)
  }, [])

  const handleDrop = useCallback(
    (e: React.DragEvent, dropIndex: number) => {
      e.preventDefault()
      if (dragIndex === null || dragIndex === dropIndex) {
        setDragIndex(null)
        setDragOverIndex(null)
        return
      }
      setEntries((prev) => {
        const next = [...prev]
        const [moved] = next.splice(dragIndex, 1)
        next.splice(dropIndex, 0, moved)
        return next
      })
      setDragIndex(null)
      setDragOverIndex(null)
      markDirty()
    },
    [dragIndex], // eslint-disable-line react-hooks/exhaustive-deps
  )

  function buildPayload(): AudienceCreateRequest | AudienceUpdateRequest {
    const entryInputs = entries.map((e) => {
      // Custom response_buttons are no longer editable in the UI — strip the key
      // from every save so persisted ones are cleaned up and Requests fall back
      // to the default Approve/Reject pair (an arbitrary option_id cannot resolve
      // an approval gate). See EntryRow.tsx for the rationale.
      const config = { ...(e.config ?? {}) } as Record<string, unknown>
      delete config['response_buttons']
      return {
        plugin_instance_id: e.plugin_instance_id,
        notify: e.notify,
        request: e.request,
        config,
      }
    })

    if (initial) {
      return {
        name,
        disable_in_app_fallback: disableInAppFallback,
        expected_version: initial.version,
        entries: entryInputs,
      } satisfies AudienceUpdateRequest
    }

    return {
      name,
      disable_in_app_fallback: disableInAppFallback,
      entries: entryInputs,
    } satisfies AudienceCreateRequest
  }

  function handleSaveClick() {
    const hasRefs =
      (references?.policies.length ?? 0) > 0 || (references?.in_flight_runs.length ?? 0) > 0

    if (hasRefs && initial) {
      setShowSaveGuard(true)
      return
    }
    void doSave()
  }

  async function doSave() {
    setIsSaving(true)
    try {
      await onSave(buildPayload())
      setDirty(false)
    } finally {
      setIsSaving(false)
      setShowSaveGuard(false)
    }
  }

  async function handleDeleteClick() {
    if (!onDelete) return
    setIsDeleting(true)
    try {
      await onDelete()
    } finally {
      setIsDeleting(false)
    }
  }

  // The synthetic in-app entry for display purposes only.
  const displayEntries: ApiAudienceEntry[] = [
    ...entries,
    ...(!disableInAppFallback
      ? [
          {
            id: '__auto__',
            plugin_instance_id: '',
            position: entries.length,
            notify: true,
            request: true,
            config: {},
            auto: true,
          },
        ]
      : []),
  ]

  const noRequestCapable =
    disableInAppFallback &&
    !entries.some((e) => e.request && !!e.plugin_instance_id)

  return (
    <div className={styles.editor}>
      {/* Version conflict banner */}
      {saveError?.status === 409 && (
        <div className={styles.conflictBanner} role="alert">
          <span>
            This audience was updated by another session. Reload to see the latest version before
            saving again.
          </span>
          <Button
            variant="ghost"
            size="small"
            onClick={() => window.location.reload()}
          >
            Reload audience
          </Button>
        </div>
      )}

      {/* Routing preview */}
      <RoutingPreview entries={displayEntries} disableInAppFallback={disableInAppFallback} pluginInstances={pluginInstances} />

      {/* Name */}
      <section className={styles.section}>
        <h2 className={styles.sectionTitle}>Name</h2>
        <div className={styles.field}>
          <label className={styles.label} htmlFor="audience-name">
            Audience name <span aria-hidden>*</span>
          </label>
          <input
            id="audience-name"
            className={styles.input}
            type="text"
            value={name}
            onChange={handleNameChange}
            disabled={!canManage}
            minLength={1}
            maxLength={64}
            required
            placeholder="e.g. ops-team"
          />
        </div>
      </section>

      {/* Entries */}
      <section className={styles.section}>
        <h2 className={styles.sectionTitle}>Entries</h2>
        <p className={styles.sectionDesc}>
          Channels are tried in order. Drag rows to reorder or use the up/down arrows.
          The synthetic <code>gleipnir.in-app</code> fallback is appended automatically unless
          explicitly disabled.
        </p>

        <ol className={styles.entryList}>
          {entries.map((entry, index) => (
            <EntryRow
              key={entry.id}
              entry={entry}
              index={index}
              totalCount={entries.length}
              pluginInstances={pluginInstances}
              onChange={(updated) => handleEntryChange(index, updated)}
              onRemove={() => handleRemoveEntry(index)}
              onMoveUp={() => handleMoveUp(index)}
              onMoveDown={() => handleMoveDown(index)}
              onDragStart={(e) => {
                e.dataTransfer.effectAllowed = 'move'
                handleDragStart(index)
              }}
              onDragOver={(e) => handleDragOver(e, index)}
              onDrop={(e) => handleDrop(e, index)}
              onDragEnd={handleDragEnd}
              isDragging={dragIndex === index}
              isDragOver={dragOverIndex === index && dragIndex !== index}
              disabled={!canManage}
            />
          ))}
          {!disableInAppFallback && (
            <EntryRow
              key="__auto__"
              entry={{
                id: '__auto__',
                plugin_instance_id: '',
                position: entries.length,
                notify: true,
                request: true,
                config: {},
                auto: true,
              }}
              index={entries.length}
              totalCount={entries.length + 1}
              pluginInstances={pluginInstances}
              onChange={() => {}}
              onRemove={() => {}}
              onMoveUp={() => {}}
              onMoveDown={() => {}}
              onDragStart={() => {}}
              onDragOver={() => {}}
              onDrop={() => {}}
              onDragEnd={() => {}}
              isDragging={false}
              isDragOver={false}
              disabled={true}
            />
          )}
        </ol>

        {canManage && (
          <Button variant="secondary" size="small" type="button" onClick={handleAddEntry}>
            + Add entry
          </Button>
        )}
      </section>

      {/* Advanced */}
      <section className={styles.section}>
        <h2 className={styles.sectionTitle}>Advanced</h2>
        <div className={styles.advancedRow}>
          <label className={styles.toggleLabel}>
            <input
              type="checkbox"
              checked={disableInAppFallback}
              onChange={handleDisableFallbackChange}
              disabled={!canManage}
            />
            Disable in-app fallback
          </label>
        </div>
        {disableInAppFallback && noRequestCapable && (
          <p className={styles.fallbackWarning}>
            Warning: no Request-capable entry and in-app fallback is disabled. Agent feedback
            requests will have no handler.
          </p>
        )}
      </section>

      {/* Save/delete errors (non-409) */}
      {saveError && saveError.status !== 409 && (
        <div className={alertStyles.alertError} role="alert">
          {saveError.detail ?? saveError.message}
        </div>
      )}
      {deleteError && deleteError.status !== 409 && (
        <div className={alertStyles.alertError} role="alert">
          {deleteError.detail ?? deleteError.message}
        </div>
      )}
      {deleteError?.status === 409 && (
        <div className={alertStyles.alertError} role="alert">
          Cannot delete: this audience is referenced by{' '}
          {deleteError.detail ?? 'one or more policies'}.
        </div>
      )}

      {/* Footer */}
      {canManage && (
        <div className={styles.footer}>
          {initial && onDelete && (
            <Button
              variant="danger"
              type="button"
              onClick={() => void handleDeleteClick()}
              disabled={isDeleting || isSaving}
            >
              {isDeleting ? 'Deleting…' : 'Delete audience'}
            </Button>
          )}
          <div className={styles.footerRight}>
            <Button
              variant="ghost"
              type="button"
              onClick={() => navigate('/admin/audiences')}
              disabled={isSaving || isDeleting}
            >
              Cancel
            </Button>
            <Button
              variant="primary"
              type="button"
              onClick={handleSaveClick}
              disabled={isSaving || isDeleting || !name.trim()}
            >
              {isSaving ? 'Saving…' : 'Save'}
            </Button>
          </div>
        </div>
      )}

      {!canManage && (
        <p className={styles.readOnlyNotice}>
          You have read-only access. Editing requires an admin or operator role.
        </p>
      )}

      {/* Save guard dialog */}
      {showSaveGuard && references && (
        <SaveGuardDialog
          references={references}
          onConfirm={() => void doSave()}
          onCancel={() => setShowSaveGuard(false)}
          isPending={isSaving}
        />
      )}
    </div>
  )
}
