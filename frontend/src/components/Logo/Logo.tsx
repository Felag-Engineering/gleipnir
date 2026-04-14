import styles from './Logo.module.css'

interface LogoProps {
  /** Predefined size variant */
  variant?: 'sidebar' | 'login'
  className?: string
}

export function Logo({ variant = 'sidebar', className }: LogoProps) {
  const classes = [
    styles.logo,
    variant === 'login' ? styles.loginSize : styles.sidebarSize,
    className,
  ].filter(Boolean).join(' ')

  return (
    <span className={classes}>
      <img
        className={styles.lightLogo}
        src="/logo-light.png"
        alt="Gleipnir"
      />
      <img
        className={styles.darkLogo}
        src="/logo-dark.png"
        alt=""
        aria-hidden="true"
      />
    </span>
  )
}
