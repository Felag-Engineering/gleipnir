import { useRef, type KeyboardEvent } from 'react'
import styles from './Tabs.module.css'

export interface TabDescriptor {
  /** Stable id used for state + aria wiring. */
  id: string
  /** Visible label. */
  label: string
  /**
   * Number of validation errors owned by this tab. A value > 0 renders an
   * error badge so operators can spot which tab needs attention without
   * opening it.
   */
  errorCount?: number
  /**
   * True when this tab's required fields are satisfied (it is not blocking
   * Save). Renders a completion check in the tab's status marker — a positive
   * signal that turns the strip into a live map of what's left. Errors take
   * precedence, so a tab with errorCount > 0 never shows as complete.
   */
  complete?: boolean
}

export interface TabsProps {
  tabs: TabDescriptor[]
  /** The id of the currently active tab. */
  activeId: string
  onChange: (id: string) => void
  /** Accessible label for the tablist container. */
  ariaLabel: string
  /**
   * Prefix for the generated tab/panel element ids. Consumers must render
   * their panels with `panelId(idPrefix, tab.id)` / `aria-labelledby` set to
   * `tabId(idPrefix, tab.id)` so the tab ↔ panel relationship is announced.
   */
  idPrefix?: string
}

/** Element id for a tab button. */
export function tabId(idPrefix: string, id: string): string {
  return `${idPrefix}-tab-${id}`
}

/** Element id for the panel a tab controls. */
export function panelId(idPrefix: string, id: string): string {
  return `${idPrefix}-panel-${id}`
}

/**
 * Accessible tablist implementing the WAI-ARIA tabs pattern:
 * roving tabindex, Arrow/Home/End keyboard navigation, and automatic
 * activation (moving focus selects the tab). Panels are rendered by the
 * consumer and stay mounted, so activation-follows-focus is the correct
 * variant.
 */
export function Tabs({ tabs, activeId, onChange, ariaLabel, idPrefix = 'tabs' }: TabsProps) {
  const btnRefs = useRef<Array<HTMLButtonElement | null>>([])

  function handleKeyDown(e: KeyboardEvent<HTMLButtonElement>, index: number) {
    let next = index
    switch (e.key) {
      case 'ArrowRight':
      case 'ArrowDown':
        next = (index + 1) % tabs.length
        break
      case 'ArrowLeft':
      case 'ArrowUp':
        next = (index - 1 + tabs.length) % tabs.length
        break
      case 'Home':
        next = 0
        break
      case 'End':
        next = tabs.length - 1
        break
      default:
        return
    }
    e.preventDefault()
    onChange(tabs[next].id)
    btnRefs.current[next]?.focus()
  }

  return (
    <div className={styles.tablist} role="tablist" aria-label={ariaLabel}>
      {tabs.map((tab, index) => {
        const selected = tab.id === activeId
        const errorCount = tab.errorCount ?? 0
        const hasError = errorCount > 0
        // Errors outrank completion: a tab that blocks Save is never "done".
        const complete = Boolean(tab.complete) && !hasError
        const markerClass = [
          styles.tabMarker,
          hasError ? styles.tabMarkerError : complete ? styles.tabMarkerComplete : '',
        ].join(' ')
        return (
          <button
            key={tab.id}
            ref={(el) => { btnRefs.current[index] = el }}
            type="button"
            role="tab"
            id={tabId(idPrefix, tab.id)}
            aria-controls={panelId(idPrefix, tab.id)}
            aria-selected={selected}
            tabIndex={selected ? 0 : -1}
            className={[styles.tab, selected ? styles.tabActive : '', hasError ? styles.tabHasError : ''].join(' ')}
            onClick={() => onChange(tab.id)}
            onKeyDown={(e) => handleKeyDown(e, index)}
          >
            <span
              className={markerClass}
              // Only the error state joins the accessible name (as it shipped in
              // #711); the numeric/complete markers are decorative so a tab's
              // name stays exactly its label for assistive tech and tests.
              aria-label={hasError ? `${errorCount} ${errorCount === 1 ? 'error' : 'errors'}` : undefined}
              aria-hidden={hasError ? undefined : true}
            >
              {hasError ? errorCount : complete ? '✓' : index + 1}
            </span>
            <span className={styles.tabLabel}>{tab.label}</span>
          </button>
        )
      })}
    </div>
  )
}
