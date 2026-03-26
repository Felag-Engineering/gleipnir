import SkeletonBlock from '@/components/SkeletonBlock/SkeletonBlock'
import styles from './QueryBoundary.module.css'

export interface SkeletonListProps {
  count?: number
  height?: number
  gap?: number
  borderRadius?: number
}

export default function SkeletonList({
  count = 5,
  height = 48,
  gap = 1,
  borderRadius = 4,
}: SkeletonListProps) {
  return (
    <div
      className={styles.skeletonList}
      style={{ '--sl-gap': gap === 1 ? '1px' : `${gap}px` } as React.CSSProperties}
    >
      {Array.from({ length: count }, (_, i) => (
        <SkeletonBlock key={i} height={height} borderRadius={borderRadius} />
      ))}
    </div>
  )
}
