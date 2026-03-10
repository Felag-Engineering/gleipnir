import styles from './SkeletonBlock.module.css'

interface Props {
  width?: string | number
  height?: string | number
  borderRadius?: string | number
  className?: string
}

// CSS custom properties bridge prop values into the CSS Module without inline styles.
// All visual rules (background, animation, overflow) live in the .module.css file.
export default function SkeletonBlock({
  width = '100%',
  height = 16,
  borderRadius = 4,
  className,
}: Props) {
  return (
    <div
      className={`${styles.block}${className ? ` ${className}` : ''}`}
      style={{
        '--skeleton-w': typeof width === 'number' ? `${width}px` : width,
        '--skeleton-h': typeof height === 'number' ? `${height}px` : height,
        '--skeleton-r': typeof borderRadius === 'number' ? `${borderRadius}px` : borderRadius,
      } as React.CSSProperties}
      aria-hidden="true"
    />
  )
}
