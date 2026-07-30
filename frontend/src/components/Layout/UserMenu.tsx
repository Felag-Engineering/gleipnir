import { useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { useNavigate } from 'react-router'
import { Settings, LogOut } from 'lucide-react'
import { logout } from '@/api/auth'
import styles from './UserMenu.module.css'

interface UserMenuProps {
  open: boolean
  onClose: () => void
}

export function UserMenu({ open, onClose }: UserMenuProps) {
  const navigate = useNavigate()
  const menuRef = useRef<HTMLDivElement>(null)
  const itemRefs = useRef<Array<HTMLButtonElement | null>>([])
  // Which item currently holds the roving tabindex=0 (all others are -1).
  const [activeIndex, setActiveIndex] = useState(0)

  // Click-outside + Escape close. Escape returns focus to the trigger via the
  // focus effect's cleanup below (open flips to false → cleanup restores focus).
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

  // Focus management, mirroring the Modal primitive's returnFocusOnDeactivate:
  // on open, remember the trigger, move focus to the first item; on close,
  // restore focus to the remembered trigger. Keyed on `open` only so a change
  // in the onClose identity can never fire the restore mid-open.
  useEffect(() => {
    if (!open) return
    const trigger = document.activeElement as HTMLElement | null
    setActiveIndex(0)
    itemRefs.current[0]?.focus()
    return () => {
      trigger?.focus()
    }
  }, [open])

  if (!open) return null

  async function handleLogout() {
    try {
      await logout()
    } finally {
      window.location.href = '/login'
    }
  }

  // Roving focus across the menu items with wraparound at both ends.
  function focusItem(index: number) {
    const count = itemRefs.current.length
    if (count === 0) return
    const next = (index + count) % count
    setActiveIndex(next)
    itemRefs.current[next]?.focus()
  }

  function handleKeyDown(e: ReactKeyboardEvent<HTMLDivElement>) {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        focusItem(activeIndex + 1)
        break
      case 'ArrowUp':
        e.preventDefault()
        focusItem(activeIndex - 1)
        break
      case 'Home':
        e.preventDefault()
        focusItem(0)
        break
      case 'End':
        e.preventDefault()
        focusItem(itemRefs.current.length - 1)
        break
    }
  }

  return (
    <div
      className={styles.menu}
      ref={menuRef}
      role="menu"
      aria-orientation="vertical"
      onKeyDown={handleKeyDown}
    >
      <button
        ref={(el) => { itemRefs.current[0] = el }}
        className={styles.menuItem}
        role="menuitem"
        tabIndex={activeIndex === 0 ? 0 : -1}
        onClick={() => { onClose(); navigate('/settings') }}
      >
        <Settings size={16} strokeWidth={1.5} />
        <span>Settings</span>
      </button>
      <div className={styles.separator} />
      <button
        ref={(el) => { itemRefs.current[1] = el }}
        className={`${styles.menuItem} ${styles.menuItemDanger}`}
        role="menuitem"
        tabIndex={activeIndex === 1 ? 0 : -1}
        onClick={handleLogout}
      >
        <LogOut size={16} strokeWidth={1.5} />
        <span>Log out</span>
      </button>
    </div>
  )
}
