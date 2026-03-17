import { useEffect, useId, type ReactNode } from 'react'
import FocusTrap from 'focus-trap-react'
import styles from './Modal.module.css'

interface Props {
  title: string
  onClose: () => void
  children: ReactNode
  footer?: ReactNode
}

export function Modal({ title, onClose, children, footer }: Props) {
  const titleId = useId()

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [onClose])

  return (
    <FocusTrap focusTrapOptions={{ initialFocus: false, allowOutsideClick: true, returnFocusOnDeactivate: true, fallbackFocus: '[role="dialog"]' }}>
      <div
        className={styles.overlay}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onClick={(e) => { if (e.target === e.currentTarget) onClose() }}
      >
        <div className={styles.box}>
          <div className={styles.header}>
            <h2 id={titleId} className={styles.title}>{title}</h2>
            <button
              type="button"
              className={styles.closeBtn}
              aria-label="Close"
              onClick={onClose}
            >
              ×
            </button>
          </div>
          <div className={styles.body}>{children}</div>
          {footer && <div className={styles.footer}>{footer}</div>}
        </div>
      </div>
    </FocusTrap>
  )
}
