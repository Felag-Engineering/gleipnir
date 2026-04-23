import { Monitor, Sun, Moon } from 'lucide-react'
import { useTheme } from '@/hooks/useTheme'
import type { ThemePreference } from '@/hooks/useTheme'
import styles from './ThemeToggle.module.css'

const CYCLE_ORDER: ThemePreference[] = ['system', 'light', 'dark']

interface ThemeToggleProps {
  compact: boolean
}

export function ThemeToggle({ compact }: ThemeToggleProps) {
  const { theme, setTheme } = useTheme()

  const cycleTheme = () => {
    const next = CYCLE_ORDER[(CYCLE_ORDER.indexOf(theme) + 1) % CYCLE_ORDER.length]
    setTheme(next)
  }

  if (compact) {
    const Icon = theme === 'light' ? Sun : theme === 'dark' ? Moon : Monitor
    const label = theme === 'light' ? 'Light theme' : theme === 'dark' ? 'Dark theme' : 'System theme'

    return (
      <button
        className={styles.compactButton}
        onClick={cycleTheme}
        aria-label={label}
        aria-pressed={true}
      >
        <Icon size={16} aria-hidden strokeWidth={1.5} />
      </button>
    )
  }

  return (
    <div className={styles.toggleGroup}>
      <button
        className={theme === 'system' ? `${styles.toggleButton} ${styles.toggleButtonActive}` : styles.toggleButton}
        onClick={() => setTheme('system')}
        aria-label="System theme"
        aria-pressed={theme === 'system'}
      >
        <Monitor size={16} aria-hidden strokeWidth={1.5} />
        <span className={styles.toggleLabel}>System</span>
      </button>
      <button
        className={theme === 'light' ? `${styles.toggleButton} ${styles.toggleButtonActive}` : styles.toggleButton}
        onClick={() => setTheme('light')}
        aria-label="Light theme"
        aria-pressed={theme === 'light'}
      >
        <Sun size={16} aria-hidden strokeWidth={1.5} />
        <span className={styles.toggleLabel}>Light</span>
      </button>
      <button
        className={theme === 'dark' ? `${styles.toggleButton} ${styles.toggleButtonActive}` : styles.toggleButton}
        onClick={() => setTheme('dark')}
        aria-label="Dark theme"
        aria-pressed={theme === 'dark'}
      >
        <Moon size={16} aria-hidden strokeWidth={1.5} />
        <span className={styles.toggleLabel}>Dark</span>
      </button>
    </div>
  )
}
