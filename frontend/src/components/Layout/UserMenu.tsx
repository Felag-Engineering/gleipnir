import { useEffect, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { Settings, Shield, LogOut } from 'lucide-react'
import { logout } from '@/api/auth'
import styles from './UserMenu.module.css'

interface UserMenuProps {
  open: boolean
  onClose: () => void
  isAdmin: boolean
}

export function UserMenu({ open, onClose, isAdmin }: UserMenuProps) {
  const navigate = useNavigate()
  const menuRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return

    function handleClickOutside(e: MouseEvent) {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        onClose()
      }
    }

    function handleEscape(e: KeyboardEvent) {
      if (e.key === 'Escape') onClose()
    }

    document.addEventListener('mousedown', handleClickOutside)
    document.addEventListener('keydown', handleEscape)
    return () => {
      document.removeEventListener('mousedown', handleClickOutside)
      document.removeEventListener('keydown', handleEscape)
    }
  }, [open, onClose])

  if (!open) return null

  async function handleLogout() {
    try {
      await logout()
    } finally {
      window.location.href = '/login'
    }
  }

  return (
    <div className={styles.menu} ref={menuRef} role="menu">
      <button
        className={styles.menuItem}
        role="menuitem"
        onClick={() => { onClose(); navigate('/settings') }}
      >
        <Settings size={16} strokeWidth={1.5} />
        <span>Settings</span>
      </button>
      {isAdmin && (
        <button
          className={styles.menuItem}
          role="menuitem"
          onClick={() => { onClose(); navigate('/settings/system') }}
        >
          <Shield size={16} strokeWidth={1.5} />
          <span>System Settings</span>
        </button>
      )}
      <div className={styles.separator} />
      <button
        className={`${styles.menuItem} ${styles.menuItemDanger}`}
        role="menuitem"
        onClick={handleLogout}
      >
        <LogOut size={16} strokeWidth={1.5} />
        <span>Log out</span>
      </button>
    </div>
  )
}
