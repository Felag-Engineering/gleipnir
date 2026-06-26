import { useCallback, useEffect, useId, useRef, useState } from 'react'
import type { ApiPluginOption } from '@/api/types'
import styles from './AsyncCombobox.module.css'

export interface AsyncComboboxProps {
  /** Unique field id used for label + aria wiring. */
  id?: string
  /**
   * Current selected value(s).
   * - single mode (multi=false or omitted): string
   * - multi mode (multi=true): string[]
   */
  value: string | string[]
  /**
   * Called when the selection changes.
   * - single mode: receives a string
   * - multi mode: receives a string[]
   */
  onChange: (value: string | string[]) => void
  /** Function invoked with the current search query; must return a stable ref. */
  onSearch: (query: string) => Promise<ApiPluginOption[]>
  /** When true, allows selecting multiple values; renders selected items as removable chips. */
  multi?: boolean
  /** Placeholder shown in the text input. */
  placeholder?: string
  /** Disabled state: falls back to a plain text input. */
  disabled?: boolean
  /** When true, shows a "(not available)" suffix on the trigger and degrades to text input. */
  degraded?: boolean
  /** aria-describedby id for error messages. */
  describedBy?: string
}

// DEBOUNCE_MS controls how long to wait after the last keystroke before
// issuing a search. Matches typical typeahead guidance (150–300ms).
const DEBOUNCE_MS = 250

// AsyncCombobox is a searchable dropdown backed by an async search function.
// It supports single-select and multi-select (chip) modes, and degrades
// gracefully to a plain text <input> when the `degraded` prop is true.
//
// Accessibility:
//  - combobox role on the text input with aria-expanded / aria-controls
//  - listbox role on the dropdown
//  - option role on each item with aria-selected / aria-disabled
//  - Keyboard: ArrowDown/Up navigate, Enter selects, Escape closes
export function AsyncCombobox({
  id,
  value,
  onChange,
  onSearch,
  multi = false,
  placeholder = 'Search…',
  disabled = false,
  degraded = false,
  describedBy,
}: AsyncComboboxProps) {
  const generatedId = useId()
  const inputId = id ?? generatedId
  const listboxId = `${inputId}-listbox`

  const selectedValues: string[] = multi
    ? Array.isArray(value)
      ? (value as string[])
      : []
    : []

  const singleValue: string = !multi
    ? typeof value === 'string'
      ? value
      : ''
    : ''

  const [open, setOpen] = useState(false)
  // inputText tracks the search query (cleared after selection in multi mode).
  const [inputText, setInputText] = useState(singleValue)
  const [options, setOptions] = useState<ApiPluginOption[]>([])
  const [loading, setLoading] = useState(false)
  const [activeIndex, setActiveIndex] = useState(-1)

  const debounceTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const containerRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)

  // Sync inputText with single-mode value when it changes externally (e.g. form reset).
  useEffect(() => {
    if (!multi) {
      setInputText(singleValue)
    }
  }, [singleValue, multi])

  const search = useCallback(
    (q: string) => {
      setLoading(true)
      setActiveIndex(-1)
      onSearch(q)
        .then((opts) => {
          setOptions(opts)
          setOpen(true)
        })
        .catch(() => {
          setOptions([])
        })
        .finally(() => {
          setLoading(false)
        })
    },
    [onSearch],
  )

  function handleInputChange(e: React.ChangeEvent<HTMLInputElement>) {
    const q = e.target.value
    setInputText(q)

    if (debounceTimer.current) clearTimeout(debounceTimer.current)
    debounceTimer.current = setTimeout(() => {
      void search(q)
    }, DEBOUNCE_MS)
  }

  function handleFocus() {
    if (debounceTimer.current) clearTimeout(debounceTimer.current)
    void search(inputText)
  }

  function handleSelect(opt: ApiPluginOption) {
    if (opt.disabled) return

    if (multi) {
      const next = selectedValues.includes(opt.value)
        ? selectedValues.filter((v) => v !== opt.value)
        : [...selectedValues, opt.value]
      onChange(next)
      // Clear the search input so the operator can keep searching.
      setInputText('')
      setOpen(false)
      setActiveIndex(-1)
      inputRef.current?.focus()
    } else {
      onChange(opt.value)
      setInputText(opt.label)
      setOpen(false)
      setActiveIndex(-1)
      inputRef.current?.blur()
    }
  }

  function handleRemoveChip(val: string) {
    onChange(selectedValues.filter((v) => v !== val))
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (!open) return
    const enabled = options.filter((o) => !o.disabled)

    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActiveIndex((prev) => Math.min(prev + 1, options.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActiveIndex((prev) => Math.max(prev - 1, -1))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      if (activeIndex >= 0 && activeIndex < options.length) {
        handleSelect(options[activeIndex])
      } else if (enabled.length === 1) {
        handleSelect(enabled[0])
      }
    } else if (e.key === 'Escape') {
      setOpen(false)
      setActiveIndex(-1)
    }
  }

  // Close dropdown when the user clicks outside the component.
  useEffect(() => {
    function handleOutsideClick(e: MouseEvent) {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setOpen(false)
        setActiveIndex(-1)
      }
    }
    document.addEventListener('mousedown', handleOutsideClick)
    return () => document.removeEventListener('mousedown', handleOutsideClick)
  }, [])

  // Degraded fallback: render a plain text input with a visual hint.
  // In multi mode, degraded falls back to the same single-text input; the
  // operator enters a single ID directly (simpler than a comma-split textarea).
  if (degraded || disabled) {
    return (
      <div className={styles.container}>
        <input
          id={inputId}
          type="text"
          className={styles.input}
          value={multi ? (Array.isArray(value) ? (value as string[]).join(', ') : '') : (value as string)}
          onChange={(e) => {
            if (multi) {
              // Split on commas for a minimal multi free-text fallback.
              onChange(
                e.target.value
                  .split(',')
                  .map((s) => s.trim())
                  .filter((s) => s.length > 0),
              )
            } else {
              onChange(e.target.value)
            }
          }}
          placeholder={degraded ? (multi ? placeholder + ' (enter IDs, comma-separated)' : placeholder + ' (type ID directly)') : placeholder}
          disabled={disabled}
          aria-describedby={describedBy}
        />
        {degraded && (
          <span className={styles.degradedHint}>
            Dynamic search unavailable — enter {multi ? 'IDs separated by commas' : 'an ID'} directly.
          </span>
        )}
      </div>
    )
  }

  // Resolve a label for a chip value: use the loaded options when available,
  // else show the raw value ID.
  function resolveChipLabel(val: string): string {
    return options.find((o) => o.value === val)?.label ?? val
  }

  // Group options by their group label for rendering.
  const grouped = groupOptions(options)

  return (
    <div className={styles.container} ref={containerRef}>
      {/* Chips row (multi mode only) */}
      {multi && selectedValues.length > 0 && (
        <div className={styles.chipRow}>
          {selectedValues.map((val) => (
            <span key={val} className={styles.chip}>
              <span className={styles.chipLabel}>{resolveChipLabel(val)}</span>
              <button
                type="button"
                className={styles.chipRemove}
                aria-label={`Remove ${resolveChipLabel(val)}`}
                onMouseDown={(e) => {
                  // Prevent the input from losing focus before we handle the click.
                  e.preventDefault()
                  handleRemoveChip(val)
                }}
              >
                ×
              </button>
            </span>
          ))}
        </div>
      )}

      <div className={styles.inputWrapper}>
        <input
          ref={inputRef}
          id={inputId}
          type="text"
          role="combobox"
          aria-expanded={open}
          aria-controls={open ? listboxId : undefined}
          aria-autocomplete="list"
          aria-activedescendant={activeIndex >= 0 ? `${listboxId}-opt-${activeIndex}` : undefined}
          aria-describedby={describedBy}
          className={styles.input}
          value={inputText}
          onChange={handleInputChange}
          onFocus={handleFocus}
          onKeyDown={handleKeyDown}
          placeholder={placeholder}
          autoComplete="off"
        />
        {loading && <span className={styles.spinner} aria-hidden="true" />}
      </div>

      {open && options.length > 0 && (
        <ul id={listboxId} role="listbox" className={styles.listbox}>
          {grouped.map(({ group, items }) => (
            <div key={group ?? '__default'}>
              {group && (
                <li role="presentation" className={styles.groupHeader}>
                  {group}
                </li>
              )}
              {items.map((opt, _i) => {
                const idx = options.indexOf(opt)
                const isActive = idx === activeIndex
                const isSelected = multi
                  ? selectedValues.includes(opt.value)
                  : opt.value === singleValue
                return (
                  <li
                    key={opt.value}
                    id={`${listboxId}-opt-${idx}`}
                    role="option"
                    aria-selected={isSelected}
                    aria-disabled={opt.disabled}
                    className={[
                      styles.option,
                      isActive ? styles.optionActive : '',
                      opt.disabled ? styles.optionDisabled : '',
                      isSelected ? styles.optionSelected : '',
                    ]
                      .filter(Boolean)
                      .join(' ')}
                    onMouseDown={(e) => {
                      // Prevent the input from losing focus before we handle the click.
                      e.preventDefault()
                      handleSelect(opt)
                    }}
                  >
                    {multi && isSelected && <span className={styles.checkMark} aria-hidden="true">✓ </span>}
                    {opt.label}
                  </li>
                )
              })}
            </div>
          ))}
        </ul>
      )}

      {open && !loading && options.length === 0 && (
        <div className={styles.emptyState}>No results</div>
      )}
    </div>
  )
}

// groupOptions collects options into an ordered list of {group, items} pairs,
// preserving the original order within each group.
function groupOptions(options: ApiPluginOption[]): { group: string | undefined; items: ApiPluginOption[] }[] {
  const seen = new Map<string | undefined, ApiPluginOption[]>()
  const order: (string | undefined)[] = []
  for (const opt of options) {
    const g = opt.group || undefined
    if (!seen.has(g)) {
      seen.set(g, [])
      order.push(g)
    }
    seen.get(g)!.push(opt)
  }
  return order.map((g) => ({ group: g, items: seen.get(g)! }))
}
