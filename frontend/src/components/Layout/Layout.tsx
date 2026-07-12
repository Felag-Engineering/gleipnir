import { useCallback, useEffect, useState } from 'react'
import FocusTrap from 'focus-trap-react'
import { Activity, Bot, ChevronUp, Cpu, History, Mail, Megaphone, Menu, Puzzle, Settings2, Users, Wrench } from 'lucide-react'
import { Logo } from '@/components/Logo/Logo'
import { ContactTray } from '@/components/ContactTray'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { useSSE } from '@/hooks/useSSE'
import { useCurrentUser } from '@/hooks/queries/users'
import { useAttentionItems } from '@/hooks/useAttentionItems'
import { useMcpServers } from '@/hooks/queries/servers'
import { ToastRegion } from '@/components/Toast'
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
  { label: 'Audiences', to: '/admin/audiences', Icon: Megaphone },
  { label: 'Plugins', to: '/admin/plugins', Icon: Puzzle },
  { label: 'System', to: '/admin/system', Icon: Settings2 },
]

export default function Layout() {
  const location = useLocation()
  const { connectionState } = useSSE()
  const { data: currentUser } = useCurrentUser()
  const [menuOpen, setMenuOpen] = useState(false)
  const handleMenuClose = useCallback(() => setMenuOpen(false), [])
  const [contactOpen, setContactOpen] = useState(false)
  const handleContactClose = useCallback(() => setContactOpen(false), [])
  const [drawerOpen, setDrawerOpen] = useState(false)
  const closeDrawer = useCallback(() => setDrawerOpen(false), [])
  const { items: attentionItems } = useAttentionItems()
  const { data: mcpServers } = useMcpServers()

  // Close the mobile drawer whenever navigation occurs so tapping a nav link
  // dismisses it. Keyed on pathname only — the drawer is a shell concern.
  useEffect(() => {
    setDrawerOpen(false)
  }, [location.pathname])

  // Esc closes the drawer (focus-trap escapeDeactivates is disabled so the
  // React state stays the source of truth, mirroring the Modal pattern).
  useEffect(() => {
    if (!drawerOpen) return
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === 'Escape') setDrawerOpen(false)
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [drawerOpen])

  // Once the viewport grows past the mobile breakpoint the sidebar is always
  // visible, so drop any lingering drawer state (and its focus trap).
  useEffect(() => {
    const mq = window.matchMedia('(min-width: 769px)')
    function onChange() {
      if (mq.matches) setDrawerOpen(false)
    }
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [])

  const hasPendingApprovals = (attentionItems?.length ?? 0) > 0
  const hasUnhealthyServers = mcpServers?.some(s => s.last_discovered_at === null) ?? false

  function navLinkClass(to: string, statusClass?: string): string {
    const active =
      to === '/agents' ? location.pathname.startsWith('/agents')
      : to === '/admin/users' ? location.pathname.startsWith('/admin/users')
      : to === '/admin/models' ? location.pathname.startsWith('/admin/models')
      : to === '/admin/audiences' ? location.pathname.startsWith('/admin/audiences')
      : to === '/admin/plugins' ? location.pathname.startsWith('/admin/plugins')
      : to === '/admin/system' ? location.pathname.startsWith('/admin/system')
      : location.pathname === to
    const base = active ? `${styles.navLink} ${styles.navLinkActive}` : styles.navLink
    return statusClass ? `${base} ${statusClass}` : base
  }

  return (
    <div className={styles.layout}>
      {drawerOpen && (
        <div
          className={styles.backdrop}
          onClick={closeDrawer}
          aria-hidden="true"
        />
      )}
      <FocusTrap
        active={drawerOpen}
        focusTrapOptions={{
          allowOutsideClick: true,
          returnFocusOnDeactivate: true,
          escapeDeactivates: false,
          fallbackFocus: '#app-sidebar',
        }}
      >
      <aside
        id="app-sidebar"
        className={drawerOpen ? `${styles.sidebar} ${styles.sidebarOpen}` : styles.sidebar}
        tabIndex={-1}
      >
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

          <button
            type="button"
            className={`${styles.navLink} ${styles.navContact}`}
            onClick={() => setContactOpen(true)}
          >
            <span className={styles.navIcon}>
              <Mail size={20} aria-hidden strokeWidth={1.5} />
            </span>
            <span className={styles.navLabel}>Contact</span>
          </button>
        </nav>

        <ContactTray open={contactOpen} onClose={handleContactClose} />

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
      </FocusTrap>

      <div className={styles.mainWrapper}>
        <div className={styles.mobileBar}>
          <button
            type="button"
            className={styles.menuToggle}
            aria-label={drawerOpen ? 'Close navigation menu' : 'Open navigation menu'}
            aria-controls="app-sidebar"
            aria-expanded={drawerOpen}
            onClick={() => setDrawerOpen(prev => !prev)}
          >
            <Menu size={22} aria-hidden strokeWidth={1.5} />
          </button>
          <div className={styles.mobileBrand}>
            <Logo variant="sidebar" />
          </div>
        </div>
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
      <ToastRegion />
    </div>
  )
}
