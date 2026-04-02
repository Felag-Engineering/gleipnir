import { useState } from 'react'
import { Activity, ChevronLeft, ChevronRight, History, ScrollText, Settings, Users, Wrench } from 'lucide-react'
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useSSE } from '@/hooks/useSSE'
import { useCurrentUser } from '@/hooks/useCurrentUser'
import { useAttentionItems } from '@/hooks/useAttentionItems'
import { useMcpServers } from '@/hooks/useMcpServers'
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
  const navigate = useNavigate()
  const { connectionState } = useSSE()
  const { data: currentUser } = useCurrentUser()
  const { items: attentionItems } = useAttentionItems()
  const { data: mcpServers } = useMcpServers()

  const hasPendingApprovals = (attentionItems?.length ?? 0) > 0
  const hasUnhealthyServers = mcpServers?.some(s => s.last_discovered_at === null) ?? false
  const unhealthyCount = mcpServers?.filter(s => s.last_discovered_at === null).length ?? 0

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

  function navLinkClass(to: string, statusClass?: string): string {
    // /policies should match all nested routes like /policies/new and /policies/:id
    const active =
      to === '/policies'
        ? location.pathname.startsWith('/policies')
        : location.pathname === to
    const base = active ? `${styles.navLink} ${styles.navLinkActive}` : styles.navLink
    return statusClass ? `${base} ${statusClass}` : base
  }

  function navTitle(label: string, to: string): string | undefined {
    if (!collapsed) return undefined
    if (to === '/dashboard' && hasPendingApprovals) {
      const n = attentionItems.length
      return `Control Center — ${n} item${n > 1 ? 's' : ''} need attention`
    }
    if (to === '/tools' && hasUnhealthyServers) {
      return `Tools — ${unhealthyCount} MCP server${unhealthyCount > 1 ? 's' : ''} unreachable`
    }
    return label
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
          }).map(({ label, to, Icon }) => {
            const statusClass =
              to === '/dashboard' && hasPendingApprovals ? styles.navLinkNeedsApproval
              : to === '/tools' && hasUnhealthyServers ? styles.navLinkMcpUnhealthy
              : undefined

            return (
              <NavLink
                key={to}
                to={to}
                className={() => navLinkClass(to, statusClass)}
                title={navTitle(label, to)}
              >
                <span className={styles.navIcon}>
                  <Icon size={20} aria-hidden strokeWidth={1.5} />
                </span>
                <span className={collapsed ? `${styles.navLabel} ${styles.navLabelHidden}` : styles.navLabel}>
                  {label}
                </span>
              </NavLink>
            )
          })}
        </nav>

        <div
          className={collapsed ? `${styles.sidebarFooter} ${styles.sidebarFooterCollapsed}` : styles.sidebarFooter}
          role="button"
          tabIndex={0}
          onClick={() => navigate('/settings')}
          onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); navigate('/settings') } }}
          aria-label="User settings"
        >
          <div className={collapsed ? `${styles.userAvatar} ${styles.userAvatarCollapsed}` : styles.userAvatar}>
            {(currentUser?.username?.[0] ?? '?').toUpperCase()}
            <span className={collapsed ? `${styles.onlineDot} ${styles.onlineDotCollapsed}` : styles.onlineDot} aria-hidden="true" />
          </div>
          {!collapsed && (
            <>
              <div className={styles.userInfo}>
                <span className={styles.userName}>{currentUser?.username ?? 'User'}</span>
                <span className={styles.userRole}>
                  {currentUser?.roles?.[0]
                    ? currentUser.roles[0].charAt(0).toUpperCase() + currentUser.roles[0].slice(1)
                    : 'User'}
                </span>
              </div>
              <span className={styles.settingsGear} aria-hidden="true">
                <Settings size={16} strokeWidth={1.5} />
              </span>
            </>
          )}
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
