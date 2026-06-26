import styles from './ResponseButtonsEditor.module.css'

export interface ResponseButton {
  option_id: string
  label: string
  value: string
  style?: 'default' | 'primary' | 'danger'
}

interface Props {
  value: ResponseButton[] | undefined
  onChange: (next: ResponseButton[] | undefined) => void
  disabled?: boolean
  idPrefix?: string
}

// ResponseButtonsEditor is a dedicated repeatable editor for the
// `response_buttons` channel config field — an array of action buttons
// presented to the recipient when Request is used. When omitted entirely the
// backend defaults to Approve/Reject buttons (spec §5.2).
//
// Removing the last row calls onChange(undefined) so the key is omitted from
// the payload and the backend default applies.
export function ResponseButtonsEditor({ value, onChange, disabled = false, idPrefix = 'rb' }: Props) {
  const buttons = value ?? []

  function handleAdd() {
    const next: ResponseButton[] = [...buttons, { option_id: '', label: '', value: '' }]
    onChange(next)
  }

  function handleRemove(index: number) {
    const next = buttons.filter((_, i) => i !== index)
    onChange(next.length > 0 ? next : undefined)
  }

  function handleFieldChange(
    index: number,
    field: keyof ResponseButton,
    newVal: string,
  ) {
    const next = buttons.map((btn, i) => {
      if (i !== index) return btn
      if (field === 'style') {
        // Blank style → omit the key entirely (S4).
        if (newVal === '') {
          const { style: _omit, ...rest } = btn
          return rest as ResponseButton
        }
        return { ...btn, style: newVal as ResponseButton['style'] }
      }
      return { ...btn, [field]: newVal }
    })
    onChange(next)
  }

  return (
    <div className={styles.editor}>
      <div className={styles.header}>
        <span className={styles.heading}>Response buttons</span>
        <span className={styles.hint}>Defaults to Approve / Reject if omitted.</span>
      </div>

      <p className={styles.description}>
        These become interactive buttons on the routed Request message in the channel
        (e.g. Slack Block Kit). The recipient&apos;s click is recorded as the response and
        flows back to the agent and audit trail.
      </p>

      <dl className={styles.legend}>
        <div className={styles.legendItem}>
          <dt className={styles.legendTerm}>ID</dt>
          <dd className={styles.legendDef}>
            Stable option identifier (<code className={styles.legendCode}>option_id</code>) sent
            back when the button is clicked.
          </dd>
        </div>
        <div className={styles.legendItem}>
          <dt className={styles.legendTerm}>Label</dt>
          <dd className={styles.legendDef}>Button text the recipient sees.</dd>
        </div>
        <div className={styles.legendItem}>
          <dt className={styles.legendTerm}>Value</dt>
          <dd className={styles.legendDef}>
            Response value recorded when clicked — what the agent and audit trail see.
          </dd>
        </div>
        <div className={styles.legendItem}>
          <dt className={styles.legendTerm}>Style</dt>
          <dd className={styles.legendDef}>Visual treatment: default, primary, or danger.</dd>
        </div>
      </dl>

      {buttons.length > 0 && (
        <div className={styles.rows}>
          {buttons.map((btn, index) => {
            const prefix = `${idPrefix}-${index}`
            return (
              <div key={index} className={styles.buttonRow}>
                <div className={styles.fields}>
                  <div className={styles.fieldGroup}>
                    <label htmlFor={`${prefix}-option_id`} className={styles.fieldLabel}>
                      ID
                    </label>
                    <input
                      id={`${prefix}-option_id`}
                      type="text"
                      className={styles.input}
                      value={btn.option_id}
                      placeholder="approve"
                      disabled={disabled}
                      onChange={(e) => handleFieldChange(index, 'option_id', e.target.value)}
                    />
                  </div>
                  <div className={styles.fieldGroup}>
                    <label htmlFor={`${prefix}-label`} className={styles.fieldLabel}>
                      Label
                    </label>
                    <input
                      id={`${prefix}-label`}
                      type="text"
                      className={styles.input}
                      value={btn.label}
                      placeholder="Approve"
                      disabled={disabled}
                      onChange={(e) => handleFieldChange(index, 'label', e.target.value)}
                    />
                  </div>
                  <div className={styles.fieldGroup}>
                    <label htmlFor={`${prefix}-value`} className={styles.fieldLabel}>
                      Value
                    </label>
                    <input
                      id={`${prefix}-value`}
                      type="text"
                      className={styles.input}
                      value={btn.value}
                      placeholder="approved"
                      disabled={disabled}
                      onChange={(e) => handleFieldChange(index, 'value', e.target.value)}
                    />
                  </div>
                  <div className={styles.fieldGroup}>
                    <label htmlFor={`${prefix}-style`} className={styles.fieldLabel}>
                      Style
                    </label>
                    <select
                      id={`${prefix}-style`}
                      className={styles.select}
                      value={btn.style ?? ''}
                      disabled={disabled}
                      onChange={(e) => handleFieldChange(index, 'style', e.target.value)}
                    >
                      <option value="">— default —</option>
                      <option value="default">default</option>
                      <option value="primary">primary</option>
                      <option value="danger">danger</option>
                    </select>
                  </div>
                </div>
                <button
                  type="button"
                  className={styles.removeBtn}
                  aria-label={`Remove button ${index + 1}`}
                  disabled={disabled}
                  onClick={() => handleRemove(index)}
                >
                  ×
                </button>
              </div>
            )
          })}
        </div>
      )}

      <button
        type="button"
        className={styles.addBtn}
        disabled={disabled}
        onClick={handleAdd}
      >
        + Add button
      </button>
    </div>
  )
}
