import { forwardRef } from 'react'
import type { ButtonHTMLAttributes } from 'react'
import spinnerStyles from '@/styles/spinner.module.css'
import styles from './Button.module.css'

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger'
  size?: 'default' | 'small'
  /**
   * When true, the button shows a spinner alongside its label, sets
   * aria-busy, and is disabled so an in-flight action cannot be
   * double-submitted. Wire this to a mutation's `isPending`.
   */
  loading?: boolean
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  {
    variant = 'primary',
    size = 'default',
    loading = false,
    className,
    type = 'button',
    disabled,
    children,
    ...rest
  },
  ref,
) {
  const classes = [
    styles.button,
    styles[variant],
    size === 'small' ? styles.small : null,
    className,
  ]
    .filter(Boolean)
    .join(' ')

  // A loading button is never clickable — disabling it is the hard guarantee
  // against double-submit (matches the ModalFooter pattern). The danger variant
  // gets a red-tinted spinner so it stays legible on its own background.
  const spinnerClass =
    variant === 'danger' ? spinnerStyles.spinnerDanger : spinnerStyles.spinner

  return (
    <button
      ref={ref}
      type={type}
      className={classes}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      {...rest}
    >
      {loading && <span className={spinnerClass} aria-hidden="true" />}
      {children}
    </button>
  )
})
