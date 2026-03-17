import { useStats } from './useStats'

export interface StatsData {
  activeRuns: number
  pendingApprovals: number
  isLoading: boolean
  isError: boolean
}

export function useStatsData(): StatsData {
  const statsQuery = useStats()
  const data = statsQuery.data

  return {
    activeRuns: data?.active_runs ?? 0,
    pendingApprovals: data?.pending_approvals ?? 0,
    isLoading: statsQuery.isLoading,
    isError: statsQuery.isError,
  }
}
