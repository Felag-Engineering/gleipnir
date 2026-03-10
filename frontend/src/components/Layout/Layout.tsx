import { NavLink, Outlet, useLocation } from 'react-router-dom'
import styles from './Layout.module.css'

export default function Layout() {
  const location = useLocation()
  const policiesActive = location.pathname.startsWith('/policies')

  return (
    <div className={styles.layout}>
      <header className={styles.topbar}>
        <div className={styles.topbarInner}>
          <span className={styles.wordmark}>GLEIPNIR</span>
          <nav className={styles.nav}>
            <NavLink
              to="/dashboard"
              className={({ isActive }) =>
                isActive ? `${styles.navLink} ${styles.navLinkActive}` : styles.navLink
              }
            >
              Dashboard
            </NavLink>
            <NavLink
              to="/policies/new"
              className={() =>
                policiesActive ? `${styles.navLink} ${styles.navLinkActive}` : styles.navLink
              }
            >
              Policies
            </NavLink>
            <NavLink
              to="/mcp"
              className={({ isActive }) =>
                isActive ? `${styles.navLink} ${styles.navLinkActive}` : styles.navLink
              }
            >
              Servers
            </NavLink>
          </nav>
        </div>
      </header>
      <main className={styles.main}>
        <Outlet />
      </main>
    </div>
  )
}
