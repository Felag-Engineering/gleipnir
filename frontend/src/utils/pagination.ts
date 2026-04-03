// Returns page numbers to display, inserting 'ellipsis' for gaps.
// Always shows first and last page, current page ±1, with ellipsis between non-adjacent groups.
export function computePageNumbers(currentPage: number, totalPages: number): (number | 'ellipsis')[] {
  if (totalPages <= 1) return [1]

  // For small page counts, show all pages with no ellipsis needed
  if (totalPages <= 7) {
    return Array.from({ length: totalPages }, (_, i) => i + 1)
  }

  const result: (number | 'ellipsis')[] = []

  // The window of pages to always show: current ±1
  const windowStart = Math.max(2, currentPage - 1)
  const windowEnd = Math.min(totalPages - 1, currentPage + 1)

  result.push(1)

  if (windowStart > 2) {
    result.push('ellipsis')
  }

  for (let p = windowStart; p <= windowEnd; p++) {
    result.push(p)
  }

  if (windowEnd < totalPages - 1) {
    result.push('ellipsis')
  }

  result.push(totalPages)

  return result
}

// Converts a range key ('1h', '24h', '7d') to an ISO timestamp for the `since` API param.
// Returns undefined for 'all' or unrecognized keys.
export function rangeToSince(range: string): string | undefined {
  const now = Date.now()
  if (range === '1h') return new Date(now - 3_600_000).toISOString()
  if (range === '24h') return new Date(now - 86_400_000).toISOString()
  if (range === '7d') return new Date(now - 7 * 86_400_000).toISOString()
  return undefined
}
