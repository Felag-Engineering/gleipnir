import { useState, useRef, useEffect, useCallback, type RefObject } from 'react'

export interface ScrollSentinelResult {
  sentinelRef: RefObject<HTMLDivElement>
  showNewPill: boolean
  scrollToBottom: () => void
}

// useScrollSentinel owns the IntersectionObserver setup for detecting whether
// the user is near the bottom of the timeline, and surfaces a "New steps" pill
// when new items arrive while the user is scrolled away.
export function useScrollSentinel(itemCount: number): ScrollSentinelResult {
  const [showNewPill, setShowNewPill] = useState(false)
  const [isNearBottom, setIsNearBottom] = useState(true)

  const sentinelRef = useRef<HTMLDivElement>(null)
  const prevItemCount = useRef(0)

  // IntersectionObserver for scroll detection (not available in all test environments)
  useEffect(() => {
    const sentinel = sentinelRef.current
    if (!sentinel || typeof IntersectionObserver === 'undefined') return
    const observer = new IntersectionObserver(
      ([entry]) => {
        setIsNearBottom(entry.isIntersecting)
      },
      { threshold: 0.1 },
    )
    observer.observe(sentinel)
    return () => observer.disconnect()
  }, [])

  // Show "New steps ↓" pill when items grow and user isn't near bottom
  useEffect(() => {
    if (itemCount > prevItemCount.current && !isNearBottom) {
      setShowNewPill(true)
    }
    prevItemCount.current = itemCount
  }, [itemCount, isNearBottom])

  const scrollToBottom = useCallback(() => {
    sentinelRef.current?.scrollIntoView({ behavior: 'smooth' })
    setShowNewPill(false)
  }, [])

  return { sentinelRef, showNewPill, scrollToBottom }
}
