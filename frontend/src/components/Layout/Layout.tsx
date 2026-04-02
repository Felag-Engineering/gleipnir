import { useState } from 'react'
import { Activity, ChevronLeft, ChevronRight, History, ScrollText, Users, Wrench } from 'lucide-react'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { useSSE } from '@/hooks/useSSE'
import { useCurrentUser } from '@/hooks/useCurrentUser'
import { ConnectionBanner } from '@/components/ConnectionBanner'
import { ThemeToggle } from '@/components/ThemeToggle'
import { McpHealthDot } from './McpHealthDot'
import styles from './Layout.module.css'

const SIDEBAR_STORAGE_KEY = 'gleipnir-sidebar-collapsed'

const NAV_ITEMS = [
  { label: 'Control Center', to: '/dashboard', Icon: Activity, requiredRole: undefined },
  { label: 'Runs', to: '/runs', Icon: History, requiredRole: undefined },
  { label: 'Policies', to: '/policies', Icon: ScrollText, requiredRole: undefined },
  { label: 'Tools', to: '/tools', Icon: Wrench, requiredRole: undefined },
  { label: 'Users', to: '/users', Icon: Users, requiredRole: 'admin' },
]

export default function Layout() {
  const location = useLocation()
  const { connectionState } = useSSE()
  const { data: currentUser } = useCurrentUser()

  // Synchronous localStorage read to avoid layout shift on page load
  const [collapsed, setCollapsed] = useState<boolean>(() => {
    try {
      return localStorage.getItem(SIDEBAR_STORAGE_KEY) === 'true'
    } catch {
      return false
    }
  })

  function toggleCollapsed() {
    const next = !collapsed
    setCollapsed(next)
    try {
      localStorage.setItem(SIDEBAR_STORAGE_KEY, String(next))
    } catch {
      // localStorage may be unavailable in private browsing
    }
  }

  function navLinkClass(to: string): string {
    // /policies should match all nested routes like /policies/new and /policies/:id
    const active =
      to === '/policies'
        ? location.pathname.startsWith('/policies')
        : location.pathname === to
    return active ? `${styles.navLink} ${styles.navLinkActive}` : styles.navLink
  }

  return (
    <div className={styles.layout}>
      <aside className={collapsed ? `${styles.sidebar} ${styles.sidebarCollapsed}` : styles.sidebar}>
        <div className={styles.sidebarHeader}>
          <span className={collapsed ? `${styles.wordmark} ${styles.wordmarkHidden}` : styles.wordmark}>
            GLEIPNIR
          </span>
          <button
            className={styles.collapseButton}
            onClick={toggleCollapsed}
            aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
          >
            {collapsed
              ? <ChevronRight size={16} aria-hidden strokeWidth={1.5} />
              : <ChevronLeft size={16} aria-hidden strokeWidth={1.5} />}
          </button>
        </div>

        <nav className={styles.nav} aria-label="Main navigation">
          {NAV_ITEMS.filter(({ requiredRole }) => {
            if (!requiredRole) return true
            return currentUser?.roles.includes(requiredRole) ?? false
          }).map(({ label, to, Icon }) => (
            <NavLink
              key={to}
              to={to}
              className={() => navLinkClass(to)}
              title={collapsed ? label : undefined}
            >
              <span className={styles.navIcon}>
                <Icon size={20} aria-hidden strokeWidth={1.5} />
              </span>
              <span className={collapsed ? `${styles.navLabel} ${styles.navLabelHidden}` : styles.navLabel}>
                {label}
              </span>
              {to === '/tools' && !collapsed && <McpHealthDot />}
            </NavLink>
          ))}
        </nav>

        <div className={styles.sidebarFooter}>
          <ThemeToggle compact={collapsed} />
          <ConnectionBanner state={connectionState} compact={collapsed} />
        </div>
      </aside>

      <div className={styles.mainWrapper}>
        <main className={styles.main}>
          <div key={location.pathname} className={styles.pageContent}>
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  )
}
