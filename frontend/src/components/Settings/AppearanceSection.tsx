import { ThemeToggle } from '@/components/ThemeToggle'
import styles from './Settings.module.css'

export function AppearanceSection() {
  return (
    <section className={styles.card}>
      <div className={styles.cardHeader}>
        <h2 className={styles.cardTitle}>Appearance</h2>
      </div>
      <div className={styles.cardBody}>
        <div className={styles.appearanceRow}>
          <span className={styles.appearanceLabel}>Theme</span>
          <ThemeToggle compact={false} />
        </div>
      </div>
    </section>
  )
}
