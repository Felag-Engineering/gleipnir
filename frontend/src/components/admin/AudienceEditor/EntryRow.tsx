import { GripVertical, ChevronUp, ChevronDown, Trash2 } from 'lucide-react'
import { PluginHealthChip } from '@/components/admin/PluginHealthChip/PluginHealthChip'
import { SchemaForm } from '@/components/form/SchemaForm'
import type { SchemaShape } from '@/components/form/SchemaForm'
import { useOptionsContext } from '@/hooks/useOptionsContext'
import { ResponseButtonsEditor } from './ResponseButtonsEditor'
import type { ResponseButton } from './ResponseButtonsEditor'
import type { ApiAudienceEntry, ApiPluginInstanceForAudience } from '@/api/types'
import styles from './EntryRow.module.css'

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
  onDragEnd: (e: React.DragEvent) => void
  isDragging: boolean
  isDragOver: boolean
  disabled: boolean
}

// Returns true when the schema property declares an array-of-objects field,
// which is the gate for rendering the ResponseButtonsEditor escape hatch (S4).
function isArrayOfObjects(prop: unknown): boolean {
  if (typeof prop !== 'object' || prop === null) return false
  const p = prop as Record<string, unknown>
  if (p['type'] !== 'array') return false
  const items = p['items']
  if (typeof items !== 'object' || items === null) return false
  return (items as Record<string, unknown>)['type'] === 'object'
}

function EntryConfig({
  selectedInstance,
  entry,
  onChange,
  isDisabled,
  index,
}: {
  selectedInstance: ApiPluginInstanceForAudience
  entry: ApiAudienceEntry
  onChange: (updated: ApiAudienceEntry) => void
  isDisabled: boolean
  index: number
}) {
  const rawSchema = selectedInstance.config_schema as SchemaShape | null | undefined
  const hasSchema =
    rawSchema != null &&
    typeof rawSchema === 'object' &&
    rawSchema.properties != null &&
    Object.keys(rawSchema.properties).length > 0

  const optionsCtx = useOptionsContext(selectedInstance.plugin_id, selectedInstance.id)

  if (!hasSchema) {
    return <p className={styles.noConfig}>No configuration required</p>
  }

  const schema = rawSchema!

  // Detect the response_buttons property in the schema (S4 strict check).
  const rbProp = schema.properties?.['response_buttons']
  const hasResponseButtons = isArrayOfObjects(rbProp)

  // Build a schema for SchemaForm that omits response_buttons — it is handled
  // by ResponseButtonsEditor. We preserve all other properties.
  const formProperties = { ...(schema.properties ?? {}) }
  if (hasResponseButtons) {
    delete formProperties['response_buttons']
  }
  const formSchema: SchemaShape = { ...schema, properties: formProperties }

  // Split entry.config: SchemaForm value excludes response_buttons; the
  // escape-hatch editor owns that key separately.
  const rb = entry.config?.['response_buttons'] as ResponseButton[] | undefined
  const schemaFormValue: Record<string, unknown> = { ...(entry.config ?? {}) }
  delete schemaFormValue['response_buttons']

  function handleSchemaFormChange(next: Record<string, unknown>) {
    const merged: Record<string, unknown> = { ...next }
    if (rb !== undefined) {
      merged['response_buttons'] = rb
    }
    onChange({ ...entry, config: merged })
  }

  function handleResponseButtonsChange(nextRb: ResponseButton[] | undefined) {
    const merged: Record<string, unknown> = { ...schemaFormValue }
    if (nextRb !== undefined) {
      merged['response_buttons'] = nextRb
    }
    onChange({ ...entry, config: merged })
  }

  return (
    <div className={isDisabled ? styles.configReadOnly : undefined}>
      <SchemaForm
        schema={formSchema}
        value={schemaFormValue}
        onChange={handleSchemaFormChange}
        fieldErrors={{}}
        idPrefix={`entry-${index}-cfg`}
        optionsContext={optionsCtx}
      />
      {hasResponseButtons && (
        <div className={styles.responseButtonsSection}>
          <ResponseButtonsEditor
            value={rb}
            onChange={handleResponseButtonsChange}
            disabled={isDisabled}
            idPrefix={`entry-${index}-rb`}
          />
        </div>
      )}
    </div>
  )
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
  onDragEnd,
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

  function handlePluginChange(e: React.ChangeEvent<HTMLSelectElement>) {
    onChange({ ...entry, plugin_instance_id: e.target.value })
  }

  function handleNotifyChange(e: React.ChangeEvent<HTMLInputElement>) {
    onChange({ ...entry, notify: e.target.checked })
  }

  function handleRequestChange(e: React.ChangeEvent<HTMLInputElement>) {
    onChange({ ...entry, request: e.target.checked })
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
      onDragEnd={onDragEnd}
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
          title={
            !canRequest
              ? 'This plugin does not implement Request'
              : "Routes an agent's feedback request to this channel."
          }
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

      {/* Config form — rendered via SchemaForm + ResponseButtonsEditor */}
      {!isAuto && selectedInstance && (
        <div className={styles.configSection}>
          <EntryConfig
            selectedInstance={selectedInstance}
            entry={entry}
            onChange={onChange}
            isDisabled={isDisabled}
            index={index}
          />
        </div>
      )}
    </li>
  )
}
