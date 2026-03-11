import { Link } from 'react-router-dom'
import styles from './EmptyState.module.css'

interface Props {
  headline: string
  subtext: string
  ctaLabel: string
  ctaTo: string
}

export default function EmptyState({ headline, subtext, ctaLabel, ctaTo }: Props) {
  return (
    <div className={styles.container}>
      <div className={styles.content}>
        <h2 className={styles.headline}>{headline}</h2>
        <p className={styles.subtext}>{subtext}</p>
        <Link to={ctaTo} className={styles.cta}>
          {ctaLabel}
        </Link>
      </div>
    </div>
  )
}
