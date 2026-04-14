import { useCallback, useState } from 'react'
import { Activity, Bot, ChevronUp, Cpu, History, Settings2, Users, Wrench } from 'lucide-react'
import { Logo } from '@/components/Logo/Logo'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { useSSE } from '@/hooks/useSSE'
import { useCurrentUser } from '@/hooks/queries/users'
import { useAttentionItems } from '@/hooks/useAttentionItems'
import { useMcpServers } from '@/hooks/queries/servers'
import { UserMenu } from './UserMenu'
import styles from './Layout.module.css'

const NAV_ITEMS = [
  { label: 'Control Center', to: '/dashboard', Icon: Activity },
  { label: 'Run History', to: '/runs', Icon: History },
  { label: 'Agents', to: '/agents', Icon: Bot },
  { label: 'Tools', to: '/tools', Icon: Wrench },
]

const ADMIN_NAV_ITEMS = [
  { label: 'Users', to: '/admin/users', Icon: Users },
  { label: 'Models', to: '/admin/models', Icon: Cpu },
  { label: 'System', to: '/admin/system', Icon: Settings2 },
]

export default function Layout() {
  const location = useLocation()
  const { connectionState } = useSSE()
  const { data: currentUser } = useCurrentUser()
  const [menuOpen, setMenuOpen] = useState(false)
  const handleMenuClose = useCallback(() => setMenuOpen(false), [])
  const { items: attentionItems } = useAttentionItems()
  const { data: mcpServers } = useMcpServers()

  const hasPendingApprovals = (attentionItems?.length ?? 0) > 0
  const hasUnhealthyServers = mcpServers?.some(s => s.last_discovered_at === null) ?? false

  function navLinkClass(to: string, statusClass?: string): string {
    const active =
      to === '/agents' ? location.pathname.startsWith('/agents')
      : to === '/admin/users' ? location.pathname.startsWith('/admin/users')
      : to === '/admin/models' ? location.pathname.startsWith('/admin/models')
      : to === '/admin/system' ? location.pathname.startsWith('/admin/system')
      : location.pathname === to
    const base = active ? `${styles.navLink} ${styles.navLinkActive}` : styles.navLink
    return statusClass ? `${base} ${statusClass}` : base
  }

  return (
    <div className={styles.layout}>
      <aside className={styles.sidebar}>
        <div className={styles.sidebarBrand}>
          <Logo variant="sidebar" />
        </div>

        <nav className={styles.nav} aria-label="Main navigation">
          {NAV_ITEMS.map(({ label, to, Icon }) => {
            const statusClass =
              to === '/dashboard' && hasPendingApprovals ? styles.navLinkNeedsApproval
              : to === '/tools' && hasUnhealthyServers ? styles.navLinkMcpUnhealthy
              : undefined

            return (
              <NavLink
                key={to}
                to={to}
                className={() => navLinkClass(to, statusClass)}
              >
                <span className={styles.navIcon}>
                  <Icon size={20} aria-hidden strokeWidth={1.5} />
                </span>
                <span className={styles.navLabel}>{label}</span>
              </NavLink>
            )
          })}
          {(currentUser?.roles?.includes('admin') ?? false) && (
            <>
              <div className={styles.navSectionHeader}>
                <span className={styles.navSectionLabel}>Admin</span>
              </div>
              {ADMIN_NAV_ITEMS.map(({ label, to, Icon }) => (
                <NavLink
                  key={to}
                  to={to}
                  className={() => navLinkClass(to)}
                >
                  <span className={styles.navIcon}>
                    <Icon size={20} aria-hidden strokeWidth={1.5} />
                  </span>
                  <span className={styles.navLabel}>{label}</span>
                </NavLink>
              ))}
            </>
          )}
        </nav>

        <div className={styles.sidebarFooterWrapper}>
          <UserMenu
            open={menuOpen}
            onClose={handleMenuClose}
          />
          <div
            className={styles.sidebarFooter}
            role="button"
            tabIndex={0}
            onClick={() => setMenuOpen(prev => !prev)}
            onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setMenuOpen(prev => !prev) } }}
            aria-label="User menu"
            aria-expanded={menuOpen}
            aria-haspopup="menu"
          >
            <div className={styles.userAvatar}>
              {(currentUser?.username?.[0] ?? '?').toUpperCase()}
              <span className={styles.onlineDot} aria-hidden="true" />
            </div>
            <div className={styles.userInfo}>
              <span className={styles.userName}>{currentUser?.username ?? 'User'}</span>
              <span className={styles.userRole}>
                {currentUser?.roles?.[0]
                  ? currentUser.roles[0].charAt(0).toUpperCase() + currentUser.roles[0].slice(1)
                  : 'User'}
              </span>
            </div>
            <span className={menuOpen ? `${styles.menuChevron} ${styles.menuChevronOpen}` : styles.menuChevron} aria-hidden="true">
              <ChevronUp size={16} strokeWidth={1.5} />
            </span>
          </div>
        </div>
      </aside>

      <div className={styles.mainWrapper}>
        {connectionState !== 'connected' && (
          <div
            className={connectionState === 'disconnected'
              ? `${styles.disconnectBanner} ${styles.disconnectBannerCritical}`
              : styles.disconnectBanner}
            role="status"
          >
            <span
              className={connectionState === 'disconnected'
                ? `${styles.disconnectDot} ${styles.disconnectDotCritical}`
                : styles.disconnectDot}
              aria-hidden="true"
            />
            {connectionState === 'reconnecting'
              ? 'Connection lost — reconnecting…'
              : 'Connection lost'}
          </div>
        )}
        <main className={styles.main}>
          <div key={location.pathname} className={styles.pageContent}>
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  )
}
