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
import { estimateCost } from '@/constants/pricing'
import styles from './CostByModelChart.module.css'

interface CostByModelChartProps {
  data: ApiTimeSeriesResponse | undefined
  isLoading: boolean
}

// Color palette for model lines, assigned by insertion order.
const MODEL_COLORS = ['#60a5fa', '#34d399', '#a78bfa', '#f59e0b']

interface ChartRow {
  time: string
  [model: string]: string | number
}

// formatYAxisDollars abbreviates dollar amounts for the Y-axis (e.g. 1200 -> "$1.2k").
function formatYAxisDollars(value: number): string {
  if (value === 0) return '$0'
  if (value >= 1000) {
    const k = value / 1000
    return k % 1 === 0 ? `$${k}k` : `$${k.toFixed(1)}k`
  }
  if (value < 1) return `$${value.toFixed(2)}`
  if (value % 1 === 0) return `$${value}`
  return `$${value.toFixed(2)}`
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

function buildChartData(data: ApiTimeSeriesResponse | undefined): { rows: ChartRow[]; models: string[] } {
  if (!data?.buckets?.length) return { rows: [], models: [] }

  // Collect all model names in the order they first appear.
  const modelSet = new Set<string>()
  for (const b of data.buckets) {
    for (const m of Object.keys(b.cost_by_model)) {
      modelSet.add(m)
    }
  }
  const models = [...modelSet]

  const rows: ChartRow[] = data.buckets.map(b => {
    const row: ChartRow = { time: formatHour(b.timestamp) }
    for (const m of models) {
      const tokens = b.cost_by_model[m] ?? 0
      row[m] = estimateCost(m, tokens)
    }
    return row
  })

  return { rows, models }
}

// formatDollars formats a dollar amount to a compact string like "$0.12".
function formatDollars(n: number): string {
  if (n === 0) return '$0.00'
  if (n < 0.01) return `$${n.toFixed(4)}`
  return `$${n.toFixed(2)}`
}

function CostLegend({ rows, models }: { rows: ChartRow[]; models: string[] }) {
  return (
    <div className={styles.legend}>
      {models.map((m, i) => {
        const total = rows.reduce((sum, r) => sum + (Number(r[m]) || 0), 0)
        const color = MODEL_COLORS[i % MODEL_COLORS.length]
        return (
          <span key={m} className={styles.legendItem}>
            <span
              className={styles.legendSwatch}
              style={{ '--swatch-color': color } as React.CSSProperties}
            />
            <span className={styles.legendLabel}>{m}</span>
            <span className={styles.legendCount}>{formatDollars(total)}</span>
          </span>
        )
      })}
      {models.length === 0 && (
        <span className={styles.legendItem}>
          <span className={styles.legendLabel}>No runs yet</span>
          <span className={styles.legendCount}>$0.00</span>
        </span>
      )}
    </div>
  )
}

export function CostByModelChart({ data, isLoading }: CostByModelChartProps) {
  const { rows, models } = buildChartData(data)
  const empty = models.length === 0

  return (
    <div className={styles.panel}>
      <div className={styles.header}>
        <span className={styles.title}>COST BY MODEL</span>
        <span className={styles.windowLabel}>rolling 24h</span>
      </div>

      <div className={styles.chartArea}>
        {isLoading ? (
          <div className={styles.skeleton} />
        ) : empty ? (
          <div className={styles.emptyState}>No runs yet</div>
        ) : (
          <ResponsiveContainer width="100%" height={160}>
            <AreaChart data={rows} margin={{ top: 4, right: 4, left: 0, bottom: 0 }}>
              <defs>
                {models.map((m, i) => {
                  const color = MODEL_COLORS[i % MODEL_COLORS.length]
                  return (
                    <linearGradient key={m} id={`grad-model-${i}`} x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor={color} stopOpacity={0.15} />
                      <stop offset="95%" stopColor={color} stopOpacity={0} />
                    </linearGradient>
                  )
                })}
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
              <YAxis
                tick={{ fontFamily: 'var(--font-mono)', fontSize: 10, fill: 'var(--text-muted)' }}
                tickLine={false}
                axisLine={false}
                width={44}
                tickFormatter={formatYAxisDollars}
              />
              <Tooltip
                formatter={(value: number) => formatDollars(value)}
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
              {models.map((m, i) => {
                const color = MODEL_COLORS[i % MODEL_COLORS.length]
                return (
                  <Area
                    key={m}
                    type="monotone"
                    dataKey={m}
                    name={m}
                    stroke={color}
                    strokeWidth={2}
                    fill={`url(#grad-model-${i})`}
                    isAnimationActive={false}
                  />
                )
              })}
            </AreaChart>
          </ResponsiveContainer>
        )}
      </div>

      {!empty && <CostLegend rows={rows} models={models} />}
    </div>
  )
}
