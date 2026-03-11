import { useEffect, useRef, useState } from 'react'
import { EditorState, Compartment } from '@codemirror/state'
import { EditorView, lineNumbers } from '@codemirror/view'
import { yaml } from '@codemirror/lang-yaml'
import { oneDark } from '@codemirror/theme-one-dark'
import { load } from 'js-yaml'
import styles from './YamlEditor.module.css'

interface YamlEditorProps {
  value: string
  onChange: (value: string) => void
  onValidityChange: (isValid: boolean) => void
  readOnly?: boolean
}

// Stable compartment reference — safe to share across instances
const editableCompartment = new Compartment()

// Custom theme override to match project design tokens
const gleipnirTheme = EditorView.theme({
  '&': {
    height: '100%',
    backgroundColor: 'var(--bg-code)',
  },
  '.cm-scroller': {
    fontFamily: 'var(--font-mono)',
    fontSize: 'var(--text-sm)',
  },
  '.cm-gutters': {
    backgroundColor: 'var(--bg-code)',
    borderRight: '1px solid var(--border-mid)',
  },
})

export function YamlEditor({ value, onChange, onValidityChange, readOnly = false }: YamlEditorProps) {
  const onChangeRef = useRef(onChange)
  onChangeRef.current = onChange
  const onValidityChangeRef = useRef(onValidityChange)
  onValidityChangeRef.current = onValidityChange
  const hostRef = useRef<HTMLDivElement>(null)
  const editorViewRef = useRef<EditorView | null>(null)
  const [validationError, setValidationError] = useState<string | null>(null)

  // Mount editor once
  useEffect(() => {
    if (!hostRef.current) return

    const view = new EditorView({
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          yaml(),
          oneDark,
          gleipnirTheme,
          editableCompartment.of(EditorView.editable.of(!readOnly)),
          EditorView.updateListener.of(update => {
            if (update.docChanged) {
              onChangeRef.current(update.state.doc.toString())
            }
          }),
        ],
      }),
      parent: hostRef.current,
    })

    editorViewRef.current = view
    return () => {
      view.destroy()
      editorViewRef.current = null
    }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Sync value from outside (controlled component guard)
  useEffect(() => {
    const view = editorViewRef.current
    if (!view) return
    if (view.state.doc.toString() === value) return
    view.dispatch({
      changes: { from: 0, to: view.state.doc.length, insert: value },
    })
  }, [value])

  // Sync readOnly
  useEffect(() => {
    const view = editorViewRef.current
    if (!view) return
    view.dispatch({
      effects: editableCompartment.reconfigure(EditorView.editable.of(!readOnly)),
    })
  }, [readOnly])

  // Validate YAML
  useEffect(() => {
    try {
      load(value)
      setValidationError(null)
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      // Truncate long error messages to keep the indicator single-line
      setValidationError(msg.length > 120 ? msg.slice(0, 120) + '…' : msg)
    }
  }, [value])

  // Fire validity callback
  useEffect(() => {
    onValidityChangeRef.current(validationError === null)
  }, [validationError])

  const isValid = validationError === null

  return (
    <div className={styles.root}>
      <div ref={hostRef} className={styles.editorHost} />
      <div className={`${styles.indicator} ${isValid ? styles.valid : styles.invalid}`}>
        {isValid ? '● Valid YAML' : `✕ Invalid YAML: ${validationError}`}
      </div>
    </div>
  )
}
