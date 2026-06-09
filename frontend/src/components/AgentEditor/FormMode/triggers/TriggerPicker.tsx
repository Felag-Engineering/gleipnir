import { useEffect, useRef, useState } from 'react'
import { ChevronDown } from 'lucide-react'
import type { ApiPluginInstanceForAudience } from '@/api/types'
import styles from './TriggerPicker.module.css'

// --- Types ---

interface BuiltinValue {
  kind: 'builtin'
  type: 'webhook' | 'manual' | 'scheduled' | 'poll' | 'cron'
}

interface SubscribedValue {
  kind: 'subscribed'
  source: string    // instance_name
  eventKind: string // EventKindDecl.Kind
}

export type TriggerPickerValue = BuiltinValue | SubscribedValue | null

export interface TriggerPickerProps {
  value: TriggerPickerValue
  onChange: (next: TriggerPickerValue) => void
  pluginInstances: ApiPluginInstanceForAudience[] | undefined
  loading: boolean
}

// --- Built-in trigger definitions ---

const BUILTIN_TRIGGERS: { type: BuiltinValue['type']; title: string; desc: string }[] = [
  { type: 'webhook',   title: 'Webhook',   desc: 'Triggered by an incoming HTTP request' },
  { type: 'manual',    title: 'Manual',    desc: 'Triggered on demand from the dashboard' },
  { type: 'scheduled', title: 'Scheduled', desc: 'Fires once at each specified date and time, then pauses' },
  { type: 'poll',      title: 'Poll',      desc: 'Periodically calls MCP tools and fires when conditions match' },
  { type: 'cron',      title: 'Cron',      desc: 'Fires on a recurring schedule defined by a cron expression' },
]

// --- Helpers ---

// labelForValue returns the display string for a TriggerPickerValue.
// The word "subscribed" is never used as a display label (ADR-048).
function labelForValue(
  v: TriggerPickerValue,
  instances: ApiPluginInstanceForAudience[] | undefined,
): string {
  if (!v) return 'Select trigger…'
  if (v.kind === 'builtin') {
    return BUILTIN_TRIGGERS.find(b => b.type === v.type)?.title ?? v.type
  }
  // Plugin-sourced: "<Plugin name> (<instance>): <event kind display>"
  const inst = instances?.find(i => i.instance_name === v.source)
  const description = inst?.event_kinds?.find(ek => ek.kind === v.eventKind)?.description ?? v.eventKind
  if (!inst) return description
  return `${inst.plugin_name ?? inst.instance_name} (${inst.instance_name}): ${description}`
}

// matchesSearch returns true when a string contains query (case-insensitive).
function matchesSearch(text: string, query: string): boolean {
  return text.toLowerCase().includes(query.toLowerCase())
}

// --- Component ---

export function TriggerPicker({ value, onChange, pluginInstances, loading }: TriggerPickerProps) {
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState('')
  const [focusedIndex, setFocusedIndex] = useState(-1)

  const containerRef = useRef<HTMLDivElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)

  // Close on outside click.
  useEffect(() => {
    if (!open) return
    function handleClick(e: MouseEvent) {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [open])

  // Auto-focus search input when opened.
  useEffect(() => {
    if (open) {
      searchRef.current?.focus()
      setFocusedIndex(-1)
    } else {
      setSearch('')
    }
  }, [open])

  // Build flat list of all visible options for keyboard navigation.
  const allOptions = buildAllOptions(pluginInstances, search)

  function handleKeyDown(e: React.KeyboardEvent) {
    if (!open) {
      if (e.key === 'Enter' || e.key === ' ' || e.key === 'ArrowDown') {
        e.preventDefault()
        setOpen(true)
      }
      return
    }
    if (e.key === 'Escape') {
      setOpen(false)
      return
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setFocusedIndex(i => Math.min(i + 1, allOptions.length - 1))
      return
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      setFocusedIndex(i => Math.max(i - 1, 0))
      return
    }
    if (e.key === 'Enter' && focusedIndex >= 0) {
      e.preventDefault()
      const opt = allOptions[focusedIndex]
      if (opt && opt.kind === 'option') selectOption(opt)
      return
    }
  }

  function selectOption(opt: FlatOption & { kind: 'option' }) {
    onChange(opt.value)
    setOpen(false)
  }

  const buttonLabel = labelForValue(value, pluginInstances)

  return (
    <div ref={containerRef} className={styles.trigger}>
      <button
        type="button"
        className={open ? `${styles.button} ${styles.buttonOpen}` : styles.button}
        onClick={() => setOpen(o => !o)}
        onKeyDown={handleKeyDown}
        aria-haspopup="listbox"
        aria-expanded={open}
      >
        <span className={styles.buttonLabel}>{buttonLabel}</span>
        <ChevronDown
          size={14}
          className={open ? `${styles.buttonCaret} ${styles.buttonCaretOpen}` : styles.buttonCaret}
          aria-hidden="true"
        />
      </button>

      {open && (
        <div className={styles.popover} role="dialog">
          <input
            ref={searchRef}
            type="text"
            className={styles.search}
            placeholder="Search triggers…"
            value={search}
            onChange={e => { setSearch(e.target.value); setFocusedIndex(-1) }}
            onKeyDown={handleKeyDown}
            aria-label="Search triggers"
          />

          <div className={styles.list} role="listbox">
            {loading ? (
              <div className={styles.skeleton}>
                <div className={styles.skeletonRow} />
                <div className={styles.skeletonRow} />
                <div className={styles.skeletonRow} />
              </div>
            ) : (
              <OptionList
                options={allOptions}
                value={value}
                focusedIndex={focusedIndex}
                onSelect={selectOption}
                onHover={idx => setFocusedIndex(idx)}
              />
            )}
          </div>
        </div>
      )}
    </div>
  )
}

// --- Internal types and helpers ---

type FlatOption =
  | { kind: 'group-header'; label: string }
  | { kind: 'option'; value: TriggerPickerValue; title: string; desc?: string; index: number }

function buildAllOptions(
  instances: ApiPluginInstanceForAudience[] | undefined,
  search: string,
): FlatOption[] {
  const result: FlatOption[] = []
  let optionIndex = 0

  // Built-in triggers group
  const matchedBuiltins = BUILTIN_TRIGGERS.filter(b =>
    !search ||
    matchesSearch(b.title, search) ||
    matchesSearch(b.desc, search),
  )

  if (matchedBuiltins.length > 0) {
    result.push({ kind: 'group-header', label: 'Built-in triggers' })
    for (const b of matchedBuiltins) {
      result.push({
        kind: 'option',
        value: { kind: 'builtin', type: b.type },
        title: b.title,
        desc: b.desc,
        index: optionIndex++,
      })
    }
  }

  // One group per plugin instance that has event_kinds
  const withEvents = (instances ?? []).filter(i =>
    i.event_kinds && i.event_kinds.length > 0,
  )

  for (const inst of withEvents) {
    const eventKinds = inst.event_kinds ?? []
    const matchedKinds = eventKinds.filter(ek =>
      !search ||
      matchesSearch(ek.kind, search) ||
      matchesSearch(ek.description ?? '', search) ||
      matchesSearch(inst.instance_name, search),
    )

    if (matchedKinds.length === 0) continue

    result.push({ kind: 'group-header', label: `${inst.plugin_name ?? inst.instance_name} (${inst.instance_name})` })
    for (const ek of matchedKinds) {
      result.push({
        kind: 'option',
        value: { kind: 'subscribed', source: inst.instance_name, eventKind: ek.kind },
        title: ek.description || ek.kind,
        index: optionIndex++,
      })
    }
  }

  return result
}

function isValueEqual(a: TriggerPickerValue, b: TriggerPickerValue): boolean {
  if (!a || !b) return a === b
  if (a.kind !== b.kind) return false
  if (a.kind === 'builtin' && b.kind === 'builtin') return a.type === b.type
  if (a.kind === 'subscribed' && b.kind === 'subscribed') {
    return a.source === b.source && a.eventKind === b.eventKind
  }
  return false
}

interface OptionListProps {
  options: FlatOption[]
  value: TriggerPickerValue
  focusedIndex: number
  onSelect: (opt: FlatOption & { kind: 'option' }) => void
  onHover: (index: number) => void
}

function OptionList({ options, value, focusedIndex, onSelect, onHover }: OptionListProps) {
  if (options.length === 0) {
    return <div className={styles.empty}>No matching triggers</div>
  }

  return (
    <>
      {options.map((opt, i) => {
        if (opt.kind === 'group-header') {
          return (
            <div key={`header-${i}`} className={styles.groupHeader} aria-hidden="true">
              {opt.label}
            </div>
          )
        }

        const selected = isValueEqual(opt.value, value)
        const focused = opt.index === focusedIndex
        const cls = [
          styles.option,
          selected ? styles.optionSelected : '',
          focused ? styles.optionFocused : '',
        ].filter(Boolean).join(' ')

        return (
          <button
            key={`opt-${i}`}
            type="button"
            role="option"
            aria-selected={selected}
            className={cls}
            onMouseDown={e => {
              // Prevent blur on the search input before click registers
              e.preventDefault()
              onSelect(opt)
            }}
            onMouseEnter={() => onHover(opt.index)}
          >
            <span className={styles.optionTitle}>{opt.title}</span>
            {opt.desc && <span className={styles.optionDesc}>{opt.desc}</span>}
          </button>
        )
      })}
    </>
  )
}
