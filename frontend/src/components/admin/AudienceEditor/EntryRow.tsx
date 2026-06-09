import Form from '@rjsf/core'
// The AJV8 validator type doesn't perfectly align with the strict generic
// constraint on Form<Record<string,unknown>>; cast to any here — AJV is
// UX-only, backend santhosh-tekuri/jsonschema is the contract (ADR spec §6).
// eslint-disable-next-line @typescript-eslint/no-explicit-any
import validatorRaw from '@rjsf/validator-ajv8'
import type { RJSFSchema } from '@rjsf/utils'

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const validator = validatorRaw as any
import { GripVertical, ChevronUp, ChevronDown, Trash2 } from 'lucide-react'
import { PluginHealthChip } from '@/components/admin/PluginHealthChip/PluginHealthChip'
import type { ApiAudienceEntry, ApiPluginInstanceForAudience } from '@/api/types'
import styles from './EntryRow.module.css'

// Suppress the default rjsf submit button via uiSchema.
const UI_SCHEMA_NO_SUBMIT = {
  'ui:submitButtonOptions': { norender: true },
}

interface Props {
  entry: ApiAudienceEntry
  index: number
  totalCount: number
  pluginInstances: ApiPluginInstanceForAudience[]
  onChange: (updated: ApiAudienceEntry) => void
  onRemove: () => void
  onMoveUp: () => void
  onMoveDown: () => void
  onDragStart: (e: React.DragEvent) => void
  onDragOver: (e: React.DragEvent) => void
  onDrop: (e: React.DragEvent) => void
  isDragging: boolean
  isDragOver: boolean
  disabled: boolean
}

export function EntryRow({
  entry,
  index,
  totalCount,
  pluginInstances,
  onChange,
  onRemove,
  onMoveUp,
  onMoveDown,
  onDragStart,
  onDragOver,
  onDrop,
  isDragging,
  isDragOver,
  disabled,
}: Props) {
  // Auto-entries (the synthetic in-app fallback) are always read-only.
  const isAuto = !!entry.auto
  const isDisabled = disabled || isAuto

  const selectedInstance = pluginInstances.find((p) => p.id === entry.plugin_instance_id)

  const canNotify = selectedInstance?.implements_notify ?? true
  const canRequest = selectedInstance?.implements_request ?? true

  // Group plugin instances by plugin_id for the <select> optgroups.
  const grouped = new Map<string, ApiPluginInstanceForAudience[]>()
  for (const pi of pluginInstances) {
    const group = grouped.get(pi.plugin_id) ?? []
    group.push(pi)
    grouped.set(pi.plugin_id, group)
  }

  const schema = selectedInstance?.config_schema as RJSFSchema | null | undefined

  function handlePluginChange(e: React.ChangeEvent<HTMLSelectElement>) {
    onChange({ ...entry, plugin_instance_id: e.target.value })
  }

  function handleNotifyChange(e: React.ChangeEvent<HTMLInputElement>) {
    onChange({ ...entry, notify: e.target.checked })
  }

  function handleRequestChange(e: React.ChangeEvent<HTMLInputElement>) {
    onChange({ ...entry, request: e.target.checked })
  }

  function handleConfigChange({ formData }: { formData?: Record<string, unknown> }) {
    onChange({ ...entry, config: formData ?? {} })
  }

  const rowClass = [
    styles.row,
    isAuto ? styles.rowAuto : '',
    isDragging ? styles.dragging : '',
    isDragOver ? styles.dragOver : '',
  ]
    .filter(Boolean)
    .join(' ')

  return (
    <li
      className={rowClass}
      draggable={!isDisabled}
      onDragStart={onDragStart}
      onDragOver={onDragOver}
      onDrop={onDrop}
    >
      <div className={styles.rowHeader}>
        {/* Drag handle */}
        <button
          type="button"
          className={styles.dragHandle}
          aria-label="Drag to reorder"
          disabled={isDisabled}
          tabIndex={-1}
        >
          <GripVertical size={14} strokeWidth={1.5} aria-hidden />
        </button>

        {/* Keyboard reorder buttons for accessibility */}
        <div className={styles.reorderButtons}>
          <button
            type="button"
            className={styles.reorderBtn}
            aria-label="Move up"
            disabled={isDisabled || index === 0}
            onClick={onMoveUp}
          >
            <ChevronUp size={12} strokeWidth={2} aria-hidden />
          </button>
          <button
            type="button"
            className={styles.reorderBtn}
            aria-label="Move down"
            disabled={isDisabled || index >= totalCount - 1}
            onClick={onMoveDown}
          >
            <ChevronDown size={12} strokeWidth={2} aria-hidden />
          </button>
        </div>

        <span className={styles.position}>{index + 1}</span>

        {/* Plugin instance picker */}
        {isAuto ? (
          <span className={styles.autoLabel}>gleipnir.in-app (built-in fallback)</span>
        ) : (
          <div className={styles.pickerWrapper}>
            <select
              className={styles.picker}
              value={entry.plugin_instance_id}
              onChange={handlePluginChange}
              disabled={isDisabled}
              aria-label={`Plugin instance for entry ${index + 1}`}
            >
              <option value="">— select plugin instance —</option>
              {Array.from(grouped.entries()).map(([pluginId, instances]) => (
                <optgroup key={pluginId} label={pluginId}>
                  {instances.map((pi) => (
                    <option key={pi.id} value={pi.id}>
                      {pi.instance_name}
                    </option>
                  ))}
                </optgroup>
              ))}
            </select>
            {selectedInstance && (
              <PluginHealthChip state={selectedInstance.state} />
            )}
          </div>
        )}

        {/* Notify toggle */}
        <label
          className={styles.toggleLabel}
          title={!canNotify ? 'This plugin does not implement Notify' : undefined}
        >
          <input
            type="checkbox"
            checked={entry.notify}
            onChange={handleNotifyChange}
            disabled={isDisabled || !canNotify}
            aria-label={`Notify for entry ${index + 1}`}
          />
          Notify
        </label>

        {/* Request toggle */}
        <label
          className={styles.toggleLabel}
          title={!canRequest ? 'This plugin does not implement Request' : undefined}
        >
          <input
            type="checkbox"
            checked={entry.request}
            onChange={handleRequestChange}
            disabled={isDisabled || !canRequest}
            aria-label={`Request for entry ${index + 1}`}
          />
          Request
        </label>

        {!isAuto && (
          <button
            type="button"
            className={styles.removeBtn}
            aria-label={`Remove entry ${index + 1}`}
            disabled={isDisabled}
            onClick={onRemove}
          >
            <Trash2 size={14} strokeWidth={1.5} aria-hidden />
          </button>
        )}
      </div>

      {/* Config form — rendered from plugin's JSON Schema via rjsf */}
      {!isAuto && selectedInstance && (
        <div className={styles.configSection}>
          {schema && Object.keys(schema).length > 0 ? (
            <Form
              schema={schema}
              validator={validator}
              formData={entry.config}
              uiSchema={UI_SCHEMA_NO_SUBMIT}
              onChange={handleConfigChange}
              disabled={isDisabled}
            />
          ) : (
            <p className={styles.noConfig}>No configuration required</p>
          )}
        </div>
      )}
    </li>
  )
}
