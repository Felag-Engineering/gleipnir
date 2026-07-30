import { Link } from 'react-router'
import styles from './EmptyState.module.css'

interface Props {
  headline: string
  subtext?: string
  ctaLabel?: string
  /** Renders the CTA as a router Link. Mutually exclusive with onCtaClick. */
  ctaTo?: string
  /** Renders the CTA as a button (e.g. to open a modal). Ignored when ctaTo is set. */
  onCtaClick?: () => void
}

export default function EmptyState({ headline, subtext, ctaLabel, ctaTo, onCtaClick }: Props) {
  return (
    <div className={styles.container}>
      <div className={styles.content}>
        <h2 className={styles.headline}>{headline}</h2>
        {subtext && <p className={styles.subtext}>{subtext}</p>}
        {ctaLabel && ctaTo && (
          <Link to={ctaTo} className={styles.cta}>
            {ctaLabel}
          </Link>
        )}
        {ctaLabel && !ctaTo && onCtaClick && (
          <button type="button" className={styles.cta} onClick={onCtaClick}>
            {ctaLabel}
          </button>
        )}
      </div>
    </div>
  )
}
