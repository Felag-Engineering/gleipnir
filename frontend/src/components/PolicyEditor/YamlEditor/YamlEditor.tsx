import { useEffect, useRef, useState } from 'react'
import { EditorState, Compartment } from '@codemirror/state'
import { EditorView, lineNumbers } from '@codemirror/view'
import { yaml } from '@codemirror/lang-yaml'
import { oneDark } from '@codemirror/theme-one-dark'
import { load } from 'js-yaml'
import { AlertCircle, CheckCircle2 } from 'lucide-react'
import { useTheme } from '@/hooks/useTheme'
import styles from './YamlEditor.module.css'

interface YamlEditorProps {
  value: string
  onChange: (value: string) => void
  onValidityChange: (isValid: boolean) => void
  readOnly?: boolean
}

// Stable compartment reference — safe to share across instances
const editableCompartment = new Compartment()

// Light theme for CodeMirror — background values here are overridden by
// gleipnirTheme (which uses CSS variables), but the non-background properties
// (caret, selection, gutter text) are unique to light mode.
const gleipnirLightTheme = EditorView.theme(
  {
    '&': {
      color: '#1e293b',
      backgroundColor: '#f1f5f9',
    },
    '.cm-content': {
      caretColor: '#1e293b',
    },
    '.cm-cursor, .cm-dropCursor': { borderLeftColor: '#1e293b' },
    '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': {
      backgroundColor: '#bfdbfe',
    },
    '.cm-activeLine': { backgroundColor: '#e2e8f040' },
    '.cm-gutters': {
      backgroundColor: '#f1f5f9',
      color: '#94a3b8',
      borderRight: '1px solid #cbd5e1',
    },
    '.cm-lineNumbers .cm-gutterElement': { color: '#94a3b8' },
  },
  { dark: false },
)

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
  const { resolvedTheme } = useTheme()
  // Each instance needs its own Compartment bound to its own EditorState
  const themeCompartmentRef = useRef(new Compartment())

  // Mount editor once
  useEffect(() => {
    if (!hostRef.current) return

    const view = new EditorView({
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          yaml(),
          themeCompartmentRef.current.of(resolvedTheme === 'dark' ? oneDark : gleipnirLightTheme),
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
    // Mount editor once; controlled value sync happens in the separate effect below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

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

  // Reconfigure CodeMirror theme when resolvedTheme changes
  useEffect(() => {
    const view = editorViewRef.current
    if (!view) return
    view.dispatch({
      effects: themeCompartmentRef.current.reconfigure(
        resolvedTheme === 'dark' ? oneDark : gleipnirLightTheme
      ),
    })
  }, [resolvedTheme])

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
        {isValid ? (
          <>
            <CheckCircle2 size={14} strokeWidth={2} aria-hidden />
            Valid YAML
          </>
        ) : (
          <>
            <AlertCircle size={14} strokeWidth={2} aria-hidden />
            {`Invalid YAML: ${validationError}`}
          </>
        )}
      </div>
    </div>
  )
}
