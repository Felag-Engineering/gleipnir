import { PageHeader } from '@/components/PageHeader'
import { usePageTitle } from '@/hooks/usePageTitle'
import styles from './SettingsPage.module.css'

export default function SystemSettingsPage() {
  usePageTitle('System Settings')

  return (
    <div className={styles.page}>
      <PageHeader title="System Settings" />
      <p style={{ color: 'var(--text-muted)', fontSize: 'var(--text-sm)' }}>
        System-wide configuration will appear here.
      </p>
    </div>
  )
}
