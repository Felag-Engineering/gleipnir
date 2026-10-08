import { SkeletonBlock } from '@/components/SkeletonBlock'
import styles from './PageFallback.module.css'

// Shown while a route's code chunk loads, using the same skeleton treatment
// pages use for their own data.
export default function PageFallback() {
  return (
    <div className={styles.fallback} role="status" aria-label="Loading page">
      <SkeletonBlock width={240} height={28} />
      <SkeletonBlock height={96} />
      <SkeletonBlock height={96} />
    </div>
  )
}
