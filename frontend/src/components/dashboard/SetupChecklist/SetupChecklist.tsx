import { ChevronDown, CircleCheckBig, Circle } from 'lucide-react'
import { Link } from 'react-router-dom'
import { useLocalStorage } from '@/hooks/useLocalStorage'
import styles from './SetupChecklist.module.css'

const COLLAPSED_STORAGE_KEY = 'gleipnir-setup-collapsed'

export interface SetupChecklistProps {
  hasModel: boolean
  hasServer: boolean
  hasAgent: boolean
  hasFirstRun: boolean
  isLoading: boolean
}

interface Step {
  label: string
  done: boolean
  href: string
  cta: string
}

export function SetupChecklist({ hasModel, hasServer, hasAgent, hasFirstRun, isLoading }: SetupChecklistProps) {
  const [collapsed, setCollapsed] = useLocalStorage(COLLAPSED_STORAGE_KEY, false)

  const allDone = hasModel && hasServer && hasAgent && hasFirstRun

  if (allDone && !isLoading) return null

  const steps: Step[] = [
    { label: 'Add a model API key', done: hasModel, href: '/admin/models', cta: 'Go to Models' },
    { label: 'Register an MCP server', done: hasServer, href: '/tools', cta: 'Go to Tools' },
    { label: 'Create an agent', done: hasAgent, href: '/agents/new', cta: 'New Agent' },
    { label: 'Trigger your first run', done: hasFirstRun, href: '/agents', cta: 'Go to Agents' },
  ]

  const doneCount = steps.filter(s => s.done).length

  function toggleCollapsed() {
    setCollapsed(!collapsed)
  }

  return (
    <section className={styles.section}>
      <button className={styles.sectionHeader} onClick={toggleCollapsed}>
        <span className={styles.sectionTitle}>SETUP</span>
        <span className={styles.countBadge}>{doneCount}/{steps.length}</span>
        <span className={`${styles.chevron} ${collapsed ? styles.chevronCollapsed : ''}`}>
          <ChevronDown size={14} aria-hidden strokeWidth={2} />
        </span>
      </button>

      {!collapsed && (
        <div className={styles.stepList}>
          {steps.map(step => (
            <div key={step.label} className={styles.stepRow}>
              <span className={step.done ? styles.iconDone : styles.iconPending}>
                {step.done
                  ? <CircleCheckBig size={16} aria-label="done" />
                  : <Circle size={16} aria-label="pending" />
                }
              </span>
              <span className={step.done ? styles.labelDone : styles.label}>
                {step.label}
              </span>
              {!step.done && (
                <Link to={step.href} className={styles.cta}>
                  {step.cta}
                </Link>
              )}
            </div>
          ))}
        </div>
      )}
    </section>
  )
}
