import { FieldError } from '@/components/form/FieldError/FieldError'
import styles from './SchemaForm.module.css'

// REDACTION_SENTINEL is the value the backend substitutes for secret fields on
// GET (ADR-049). Bulk PUT rejects it — call sites strip such fields before
// sending. Exported so payload builders can reference the same constant.
export const REDACTION_SENTINEL = '***'

export interface SchemaProperty {
  type?: string
  items?: { type?: string }
  description?: string
  // ADR-049: fields annotated with x-gleipnir-secret: true are redacted on read
  // and must be submitted via a separate write (never round-tripped as "***").
  'x-gleipnir-secret'?: boolean
}

export interface SchemaShape {
  properties?: Record<string, SchemaProperty>
}

export interface SchemaFormProps {
  schema: SchemaShape
  value: Record<string, unknown>
  onChange: (next: Record<string, unknown>) => void
  fieldErrors: Record<string, string>
  // idPrefix scopes generated DOM ids so multiple SchemaForms on one page don't collide.
  idPrefix?: string
  // emptyMessage is shown when the schema declares no properties.
  emptyMessage?: string
}

function isStringArrayProp(prop: SchemaProperty): boolean {
  return prop.type === 'array' && prop.items?.type === 'string'
}

// SchemaForm renders a dynamic form driven by a JSON Schema's `properties` map.
// Supported leaf types: string, string[] (array of strings), boolean, number.
// Secret string fields (`x-gleipnir-secret: true`) render as password inputs
// with sentinel-aware placeholders.
//
// Field changes are bubbled via onChange so the parent owns state — this
// component is fully controlled.
export function SchemaForm({
  schema,
  value,
  onChange,
  fieldErrors,
  idPrefix = 'field',
  emptyMessage = 'No fields declared in schema.',
}: SchemaFormProps) {
  const properties = schema.properties ?? {}
  const fields = Object.keys(properties)

  if (fields.length === 0) {
    return <p className={styles.noFields}>{emptyMessage}</p>
  }

  function handleChange(name: string, newVal: unknown) {
    onChange({ ...value, [name]: newVal })
  }

  return (
    <div className={styles.schemaFields}>
      {fields.map((name) => {
        const prop = properties[name]
        const fieldId = `${idPrefix}-${name}`
        const fieldErrId = `${idPrefix}-err-${name}`
        const err = fieldErrors[name]
        const isSecret = !!prop['x-gleipnir-secret']

        if (prop.type === 'boolean') {
          return (
            <div key={name} className={styles.fieldRow}>
              <label className={styles.checkboxLabel}>
                <input
                  type="checkbox"
                  checked={!!value[name]}
                  onChange={(e) => handleChange(name, e.target.checked)}
                />
                <span>{name}</span>
              </label>
              {prop.description && <p className={styles.fieldDesc}>{prop.description}</p>}
              <FieldError id={fieldErrId} messages={err} />
            </div>
          )
        }

        if (prop.type === 'number' || prop.type === 'integer') {
          return (
            <div key={name} className={styles.fieldRow}>
              <label htmlFor={fieldId} className={styles.fieldLabel}>
                {name}
              </label>
              {prop.description && <p className={styles.fieldDesc}>{prop.description}</p>}
              <input
                id={fieldId}
                type="number"
                className={styles.input}
                value={typeof value[name] === 'number' ? (value[name] as number) : ''}
                onChange={(e) => handleChange(name, e.target.valueAsNumber)}
                aria-describedby={err ? fieldErrId : undefined}
              />
              <FieldError id={fieldErrId} messages={err} />
            </div>
          )
        }

        if (isStringArrayProp(prop)) {
          // Render string[] as a textarea with one entry per line.
          const rawArr = value[name]
          const displayVal = Array.isArray(rawArr) ? (rawArr as string[]).join('\n') : ''
          return (
            <div key={name} className={styles.fieldRow}>
              <label htmlFor={fieldId} className={styles.fieldLabel}>
                {name}
                <span className={styles.fieldHint}> (one per line)</span>
              </label>
              {prop.description && <p className={styles.fieldDesc}>{prop.description}</p>}
              <textarea
                id={fieldId}
                className={styles.textarea}
                rows={4}
                value={displayVal}
                onChange={(e) =>
                  handleChange(
                    name,
                    e.target.value
                      .split('\n')
                      .map((s) => s.trim())
                      .filter((s) => s.length > 0),
                  )
                }
                aria-describedby={err ? fieldErrId : undefined}
              />
              <FieldError id={fieldErrId} messages={err} />
            </div>
          )
        }

        if (isSecret) {
          // Server returns REDACTION_SENTINEL for existing values — show empty
          // input with a placeholder so the operator must explicitly type to
          // change the value (prevents sentinel round-trip; ADR-049).
          const currentVal = typeof value[name] === 'string' ? (value[name] as string) : ''
          const isSentinel = currentVal === REDACTION_SENTINEL
          return (
            <div key={name} className={styles.fieldRow}>
              <label htmlFor={fieldId} className={styles.fieldLabel}>
                {name}
                <span className={styles.fieldHint}> (secret)</span>
              </label>
              {prop.description && <p className={styles.fieldDesc}>{prop.description}</p>}
              <input
                id={fieldId}
                type="password"
                className={styles.input}
                value={isSentinel ? '' : currentVal}
                placeholder={isSentinel ? '(already set — leave blank to keep)' : ''}
                autoComplete="new-password"
                onChange={(e) => handleChange(name, e.target.value)}
                aria-describedby={err ? fieldErrId : undefined}
              />
              <FieldError id={fieldErrId} messages={err} />
            </div>
          )
        }

        // Default: plain string input.
        return (
          <div key={name} className={styles.fieldRow}>
            <label htmlFor={fieldId} className={styles.fieldLabel}>
              {name}
            </label>
            {prop.description && <p className={styles.fieldDesc}>{prop.description}</p>}
            <input
              id={fieldId}
              type="text"
              className={styles.input}
              value={typeof value[name] === 'string' ? (value[name] as string) : ''}
              onChange={(e) => handleChange(name, e.target.value)}
              aria-describedby={err ? fieldErrId : undefined}
            />
            <FieldError id={fieldErrId} messages={err} />
          </div>
        )
      })}
    </div>
  )
}
