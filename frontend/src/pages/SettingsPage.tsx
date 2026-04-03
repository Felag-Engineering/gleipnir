import { PageHeader } from '@/components/PageHeader'
import { usePageTitle } from '@/hooks/usePageTitle'
import { AppearanceSection } from '@/components/Settings/AppearanceSection'
import { ChangePasswordSection } from '@/components/Settings/ChangePasswordSection'
import { DefaultModelSection } from '@/components/Settings/DefaultModelSection'
import { DateTimeSection } from '@/components/Settings/DateTimeSection'
import { SessionsSection } from '@/components/Settings/SessionsSection'
import styles from './SettingsPage.module.css'

export default function SettingsPage() {
  usePageTitle('Settings')

  return (
    <div className={styles.page}>
      <PageHeader title="Settings" />
      <AppearanceSection />
      <ChangePasswordSection />
      <DefaultModelSection />
      <DateTimeSection />
      <SessionsSection />
    </div>
  )
}
