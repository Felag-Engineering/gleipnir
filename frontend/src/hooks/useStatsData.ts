import { useMemo } from 'react';
import { useStats } from './useStats';
import { makeDashboardStats } from '../components/dashboard/StatsBar';
import type { Stat } from '../components/dashboard/StatsBar';

export interface StatsData {
  stats: Stat[];
  isLoading: boolean;
  isError: boolean;
}

export function useStatsData(): StatsData {
  const statsQuery = useStats();

  const stats = useMemo(() => {
    const data = statsQuery.data;
    const activeRuns = data?.active_runs ?? 0;
    const pendingApprovals = data?.pending_approvals ?? 0;
    const policyCount = data?.policy_count ?? 0;
    const tokensToday = data?.tokens_last_24h ?? 0;
    return makeDashboardStats(activeRuns, pendingApprovals, policyCount, tokensToday);
  }, [statsQuery.data]);

  return {
    stats,
    isLoading: statsQuery.isLoading,
    isError: statsQuery.isError,
  };
}
