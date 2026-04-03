import { useSearchParams } from 'react-router-dom'

export interface RunsFilters {
  status: string
  policy: string
  range: string
  sort: string
  page: number
  setFilter: (key: string, value: string) => void
  goToPage: (p: number) => void
  toggleSort: () => void
}

// Owns all URL-based filter state for the runs list page.
// Reads status, policy, range, sort, and page from the URL and exposes
// mutation helpers that keep params in sync.
export function useRunsFilters(): RunsFilters {
  const [searchParams, setSearchParams] = useSearchParams()

  const status = searchParams.get('status') ?? ''
  const policy = searchParams.get('policy') ?? ''
  const range = searchParams.get('range') ?? 'all'
  const sort = searchParams.get('sort') ?? 'newest'
  const page = Math.max(1, parseInt(searchParams.get('page') ?? '1', 10))

  function setFilter(key: string, value: string) {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev)
      if (value) {
        next.set(key, value)
      } else {
        next.delete(key)
      }
      // Reset to page 1 whenever a filter changes
      next.delete('page')
      return next
    })
  }

  function goToPage(p: number) {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev)
      next.set('page', String(p))
      return next
    })
  }

  function toggleSort() {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev)
      const current = prev.get('sort') ?? 'newest'
      next.set('sort', current === 'newest' ? 'oldest' : 'newest')
      next.delete('page')
      return next
    })
  }

  return { status, policy, range, sort, page, setFilter, goToPage, toggleSort }
}
