import { useMemo } from 'react';
import { usePolicies } from './usePolicies';
import { makeDashboardStats } from '../components/dashboard/StatsBar';
import type { Stat } from '../components/dashboard/StatsBar';

// Active run statuses — used to derive active/pending counts from each policy's latest_run.
// Note: this counts policies with an active latest run, not total concurrent runs.
// A dedicated useRuns({ status }) hook would give exact counts when available.
const ACTIVE_STATUSES = new Set(['running', 'pending']);
const APPROVAL_STATUS = 'waiting_for_approval';

export interface StatsData {
  stats: Stat[];
  isLoading: boolean;
  isError: boolean;
}

export function useStatsData(): StatsData {
  const policies = usePolicies();

  const stats = useMemo(() => {
    const items = policies.data ?? [];
    const activeRuns = items.filter(p => ACTIVE_STATUSES.has(p.latest_run?.status ?? '')).length;
    const pendingApprovals = items.filter(p => p.latest_run?.status === APPROVAL_STATUS).length;
    const policyCount = items.length;
    const tokensToday = items.reduce((sum, p) => sum + (p.latest_run?.token_cost ?? 0), 0);
    return makeDashboardStats(activeRuns, pendingApprovals, policyCount, tokensToday);
  }, [policies.data]);

  return {
    stats,
    isLoading: policies.isLoading,
    isError: policies.isError,
  };
}
