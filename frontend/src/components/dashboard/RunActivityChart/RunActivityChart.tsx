import {
  AreaChart,
  Area,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  CartesianGrid,
} from 'recharts'
import type { ApiTimeSeriesResponse } from '@/api/types'
import styles from './RunActivityChart.module.css'

interface RunActivityChartProps {
  data: ApiTimeSeriesResponse | undefined
  isLoading: boolean
}

interface ChartRow {
  time: string
  completed: number
  approval: number
  failed: number
}

// formatHour turns an ISO timestamp into a short clock label like "14:00".
function formatHour(iso: string): string {
  try {
    const d = new Date(iso)
    return d.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', hour12: false })
  } catch {
    return iso
  }
}

function buildChartData(data: ApiTimeSeriesResponse | undefined): ChartRow[] {
  if (!data?.buckets?.length) return []
  return data.buckets.map(b => ({
    time: formatHour(b.timestamp),
    completed: b.completed,
    approval: b.waiting_for_approval,
    failed: b.failed,
  }))
}

// Legend component that shows colored swatch + label + 24h total count.
function ChartLegend({ rows }: { rows: ChartRow[] }) {
  const total = (key: keyof Omit<ChartRow, 'time'>) =>
    rows.reduce((sum, r) => sum + r[key], 0)

  const items = [
    { key: 'completed' as const, label: 'Completed', color: '#4ade80' },
    { key: 'approval' as const, label: 'Needs Approval', color: '#fb923c' },
    { key: 'failed' as const, label: 'Failed', color: '#f87171' },
  ]

  return (
    <div className={styles.legend}>
      {items.map(({ key, label, color }) => (
        <span key={key} className={styles.legendItem}>
          <span className={styles.legendSwatch} style={{ '--swatch-color': color } as React.CSSProperties} />
          <span className={styles.legendLabel}>{label}</span>
          <span className={styles.legendCount}>{total(key)}</span>
        </span>
      ))}
    </div>
  )
}

export function RunActivityChart({ data, isLoading }: RunActivityChartProps) {
  const rows = buildChartData(data)

  return (
    <div className={styles.panel}>
      <div className={styles.header}>
        <span className={styles.title}>RUN ACTIVITY</span>
        <span className={styles.windowLabel}>rolling 24h</span>
      </div>

      <div className={styles.chartArea}>
        {isLoading ? (
          <div className={styles.skeleton} />
        ) : (
          <ResponsiveContainer width="100%" height={160}>
            <AreaChart data={rows} margin={{ top: 4, right: 4, left: -20, bottom: 0 }}>
              <defs>
                <linearGradient id="grad-completed" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor="#4ade80" stopOpacity={0.15} />
                  <stop offset="95%" stopColor="#4ade80" stopOpacity={0} />
                </linearGradient>
                <linearGradient id="grad-approval" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor="#fb923c" stopOpacity={0.15} />
                  <stop offset="95%" stopColor="#fb923c" stopOpacity={0} />
                </linearGradient>
                <linearGradient id="grad-failed" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor="#f87171" stopOpacity={0.15} />
                  <stop offset="95%" stopColor="#f87171" stopOpacity={0} />
                </linearGradient>
              </defs>
              <CartesianGrid
                vertical={false}
                stroke="rgba(30, 35, 48, 0.3)"
                strokeDasharray=""
              />
              <XAxis
                dataKey="time"
                tick={{ fontFamily: 'var(--font-mono)', fontSize: 10, fill: 'var(--text-muted)' }}
                tickLine={false}
                axisLine={false}
                interval="preserveStartEnd"
              />
              <YAxis hide />
              <Tooltip
                contentStyle={{
                  background: 'var(--bg-elevated)',
                  border: '1px solid var(--border-mid)',
                  borderRadius: '6px',
                  fontFamily: 'var(--font-mono)',
                  fontSize: 12,
                }}
                labelStyle={{ color: 'var(--text-second)' }}
                itemStyle={{ color: 'var(--text-primary)' }}
              />
              <Area
                type="monotone"
                dataKey="completed"
                name="Completed"
                stroke="#4ade80"
                strokeWidth={2}
                fill="url(#grad-completed)"
                isAnimationActive={false}
              />
              <Area
                type="monotone"
                dataKey="approval"
                name="Needs Approval"
                stroke="#fb923c"
                strokeWidth={2}
                fill="url(#grad-approval)"
                isAnimationActive={false}
              />
              <Area
                type="monotone"
                dataKey="failed"
                name="Failed"
                stroke="#f87171"
                strokeWidth={1.5}
                strokeDasharray="4 2"
                fill="url(#grad-failed)"
                isAnimationActive={false}
              />
            </AreaChart>
          </ResponsiveContainer>
        )}
      </div>

      <ChartLegend rows={rows} />
    </div>
  )
}
