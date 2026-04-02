# Dashboard Redesign: Control Center

**Date:** 2026-04-02
**Status:** Approved for implementation
**Scope:** Rename Dashboard to Control Center, replace stat cards and onboarding with three-zone layout (dual line charts, attention queue, recent runs), add sidebar MCP health indicator.

---

## 1. Overview

The current Dashboard page shows three generic stat cards (Active Runs, Pending Approvals, System Health) and an onboarding checklist. This redesign replaces it with a professional ops-focused Control Center that proves the system is alive through real-time line charts and surfaces only what the operator needs to act on.

**Design philosophy:** Respect operator time and attention. Show proof the system is working (line charts with movement), surface actionable items only when they exist, and keep everything else out of the way.

**Visual mockup:** `.superpowers/brainstorm/240960-1775101308/content/dashboard-mockup.html` — open locally for reference.

---

## 2. Page Rename

- Rename "Dashboard" to **"Control Center"** everywhere: sidebar label, page title, `<title>` tag, route comments.
- Change sidebar icon from the current grid/layout icon to Lucide **`Activity`** (heartbeat/EKG line).
- Route remains `/dashboard` (no URL change needed — avoid breaking bookmarks).
- The `<title>` tag becomes `Gleipnir — Control Center`.

---

## 3. Layout Structure

The page keeps the existing `max-width: 1200px` centered layout. White space on the right is intentional (Ma principle — only what's needed, no noise).

```
┌──────────────────────────────────────────────┐
│  Control Center                              │  ← Page header (no action buttons)
├──────────────────────┬───────────────────────┤
│  Run Activity        │  Cost by Model        │  ← Zone 1: Dual line charts
│  (rolling 24h)       │  (rolling 24h)        │
│  ── Completed (38)   │  ── Sonnet 4 ($0.12)  │
│  ── Approval (4)     │  ── Haiku 3.5 ($0.04) │
│  ── Failed (2)       │  ── Opus 4 ($0.31)    │
├──────────────────────┴───────────────────────┤
│  ▾ Needs Attention (3)                       │  ← Zone 2: Collapsible
│  ┌─ ■ Approval  deploy-staging  2:34  [A][R] │
│  ├─ ■ Question  log-anomalies  14:22  [Resp] │
│  └─ ■ Failed    backup-db      [View] [×]    │
├──────────────────────────────────────────────┤
│  Recent Runs                   View all → │  ← Zone 3: Run feed
│  deploy-staging    ● Approval   —    2.1k    │
│  log-anomalies     ● Running    1:12  890    │
│  sync-github       ● Complete   45s   1.4k   │
│  ...                                         │
└──────────────────────────────────────────────┘
```

---

## 4. Zone 1: Dual Line Charts

Two side-by-side panels in a `grid-template-columns: 1fr 1fr` layout with `gap: 16px`.

### 4.1 Run Activity Chart (left panel)

- **Type:** Area line chart (lines with subtle gradient fill beneath)
- **Time window:** Rolling 24 hours, x-axis shows clock times (00:00, 04:00, 08:00, ... now)
- **Data:** Cumulative run counts bucketed into hourly intervals, segmented by terminal status
- **Three lines:**
  - **Completed** — `var(--color-green)` #4ade80, solid 2px line
  - **Needs Approval** — `var(--color-orange)` #fb923c, solid 2px line
  - **Failed** — `var(--color-red)` #f87171, dashed 1.5px line
- **Legend:** Below chart, horizontal. Each item: colored line swatch + label + **count in monospace bold**. Example: `── Completed 38`. Counts are 24h totals.
- **Grid lines:** Subtle horizontal lines at even intervals, `var(--border-subtle)` at 30% opacity.
- **Y-axis:** Implicit from gridlines. No axis labels needed (operator cares about shape, not exact numbers — hover tooltips provide detail).
- **Area fills:** Linear gradient from line color at 15% opacity to transparent, beneath each line.

### 4.2 Cost by Model Chart (right panel)

- **Type:** Area line chart, same visual style as Run Activity
- **Time window:** Rolling 24 hours, same x-axis alignment
- **Data:** Dollar cost accumulated per hour, segmented by model name
- **Lines:** One line per model used in the 24h window. Colors assigned from a fixed palette:
  - First model: `var(--color-blue)` #60a5fa
  - Second model: `var(--color-teal)` #34d399
  - Third model: `var(--color-purple)` #a78bfa
  - Fourth+ model: `var(--color-amber)` #f59e0b (unlikely at homelab scale)
- **Legend:** Same style as Run Activity. Each item: line swatch + model display name + **dollar amount in monospace bold**. Example: `── Sonnet 4 $0.12`.
- **Cost calculation:** Client-side. Token counts from run data multiplied by per-model pricing constants. Pricing lives in a frontend constants file and can be updated without backend changes. The model name must come from the run record (see Section 8 for backend changes).

### 4.3 Chart Library

- **Install `recharts`** as a production dependency.
- Use `<AreaChart>`, `<Area>`, `<XAxis>`, `<YAxis>`, `<Tooltip>`, `<Legend>` components.
- Custom theme via recharts props to match Gleipnir tokens (font family, colors, grid style).
- Custom legend component rendering the inline count/dollar format (recharts default legend won't match).
- `<Tooltip>` on hover shows exact values at a time point.

### 4.4 Chart Panel Styling

- Background: `var(--bg-surface)`
- Border: `1px solid var(--border-subtle)`, `border-radius: 8px`
- Padding: `20px 24px`
- Chart header: title (uppercase monospace 13px `var(--text-second)`) + window label ("rolling 24h" in monospace 11px `var(--text-muted)`)
- Chart area height: ~160px
- Legend: below chart, `gap: 20px` between items

### 4.5 Empty State

When there are zero runs in the last 24 hours:
- Charts render with flat zero lines and empty gridlines (not a blank box)
- Legend shows all categories at 0 / $0.00
- No overlay message — the flat lines themselves communicate "nothing happening"

### 4.6 Responsive Behavior

- Below 768px: Zone 1 switches to single column (`grid-template-columns: 1fr`), charts stack vertically.

---

## 5. Zone 2: Needs Attention

A collapsible section that surfaces items requiring operator action, sorted by urgency.

### 5.1 Header

- Section title: "NEEDS ATTENTION" (uppercase, 13px semibold, `var(--text-second)`)
- Count badge: orange pill showing total item count. Font: monospace 11px. Background: `color-mix(in srgb, var(--color-orange) 15%, transparent)`.
- Chevron toggle: `▾` when expanded, rotated 90deg when collapsed. Clicking header row toggles.
- Collapse state: stored in `localStorage` key `gleipnir-attention-collapsed`. Default: expanded.

### 5.2 Item Types and Colors

Three item types, each with a colored left accent bar (4px wide, full height, border-radius 2px):

| Type | Accent Color | Badge Color | Label | Source |
|---|---|---|---|---|
| Approval | `var(--color-orange)` | orange | "APPROVAL" | `approval_requests` with status `pending` |
| Question | `var(--color-purple)` | purple | "QUESTION" | `feedback_requests` with status `pending` |
| Failure | `var(--color-red)` | red | "FAILED" | `runs` with status `failed` in last 24h, not dismissed |

### 5.3 Sort Order

All items sorted by **deadline urgency** (ascending — soonest deadline first):
- **Approvals:** sort by `expires_at`
- **Questions:** sort by `expires_at` (if set), otherwise by `created_at + 24h`
- **Failures:** sort by `created_at + 24h` (auto-dismiss deadline)

### 5.4 Item Layout

Each item is a card with:
```
┌─[accent]─ [TYPE BADGE] policy-name ─────────── [countdown] [actions]─┐
│           Detail text / tool name / message                          │
└──────────────────────────────────────────────────────────────────────┘
```

- **Grid:** `grid-template-columns: 4px 1fr auto`
- **Type badge:** Uppercase monospace 11px, colored background pill
- **Policy name:** 14px medium weight, `var(--text-primary)`. Linked to the run detail page.
- **Detail line:** 13px `var(--text-second)`. Content varies:
  - Approval: `Tool <code>tool_name</code> requires approval`
  - Question: The agent's message text (truncated to ~120 chars)
  - Failure: Error message from the run (truncated)
- **Countdown:** Monospace 13px. Shows `MM:SS` remaining until `expires_at`. Color: `var(--color-amber)` normally, `var(--color-red)` with pulse animation when < 2 minutes.
- **Actions by type:**
  - Approval: `[Approve]` button (green ghost) + `[Reject]` button (subtle ghost)
  - Question: `[Respond]` button (purple ghost) — opens run detail where they can type a response
  - Failure: `[View Run]` button (subtle ghost) + `[×]` dismiss button

### 5.5 Dismiss Behavior

Failed run items auto-dismiss when:
1. The same policy completes a new run (any terminal status replaces the failure)
2. 24 hours pass since the failure's `created_at`
3. Operator clicks the dismiss `[×]` button

Dismissed failures are tracked in `localStorage` as a set of run IDs: `gleipnir-dismissed-failures`.

Approvals and questions disappear automatically when resolved (SSE events trigger cache invalidation).

### 5.6 Empty State

When no items need attention, the entire Zone 2 section is **hidden** — no header, no empty card, nothing. The absence is the signal. Zone 3 moves up to fill the space.

### 5.7 Inline Approval Actions

Approve/Reject buttons in Zone 2 call the existing `POST /api/v1/runs/{runID}/approval` endpoint directly. On success, the item animates out (fade + collapse, 200ms ease-out) and the count badge updates. On error, show an inline error message on the item.

### 5.8 Data Source

Zone 2 combines data from three queries:
- Pending approvals: `GET /api/v1/runs?status=waiting_for_approval&limit=10`
- Pending feedback: `GET /api/v1/runs?status=waiting_for_feedback&limit=10`
- Recent failures: `GET /api/v1/runs?status=failed&since={24h_ago}&limit=10&sort=started_at&order=desc`

These are merged client-side, sorted by urgency, and filtered against the dismissed set.

---

## 6. Zone 3: Recent Runs Feed

A compact table showing the most recent runs.

### 6.1 Header

- Title: "RECENT RUNS" (same style as other zone headers)
- "View all runs →" link aligned right, `var(--color-blue)`, navigates to `/runs`

### 6.2 Table Layout

- Container: `var(--bg-surface)` background, `1px solid var(--border-subtle)` border, `border-radius: 8px`
- Row grid: `grid-template-columns: minmax(160px, 1.5fr) 100px 80px 80px 1fr`
- Columns: Policy Name | Status | Duration | Tokens | Time
- Row padding: `12px 20px`
- Row separator: `1px solid var(--border-subtle)` (none on last row)
- Row hover: `var(--bg-elevated)` background
- Row click: navigates to `/runs/{id}`

### 6.3 Column Details

| Column | Font | Color | Format |
|---|---|---|---|
| Policy Name | 14px medium | `var(--text-primary)` | Truncate with ellipsis |
| Status | 12px medium | Status color | Colored dot + label (reuse StatusBadge logic) |
| Duration | mono 12px | `var(--text-second)` | `formatDuration()` or `—` if still running |
| Tokens | mono 12px | `var(--text-muted)` | `formatTokens()` |
| Time | 12px | `var(--text-muted)` | `formatTimeAgo()`, right-aligned |

### 6.4 Data Source

- `GET /api/v1/runs?limit=10&sort=started_at&order=desc`
- Refreshed via TanStack Query. SSE `run.status_changed` and `run.step_added` events invalidate the cache.

### 6.5 Empty State

When no runs exist at all, show the existing `EmptyState` component: "No runs yet. Create a policy and trigger your first run." with a link to `/policies`.

---

## 7. Sidebar MCP Health Indicator

### 7.1 Behavior

- The **Tools** nav item in the sidebar gets a conditional orange pulsing dot when any MCP server is considered unhealthy.
- Healthy = `last_discovered_at` is not null (server has been discovered at least once). Unhealthy determination requires a new backend field (see Section 8).
- The dot is an 8px circle, `var(--color-orange)`, with a `box-shadow` glow and a `pulse` animation (2s ease-in-out infinite, opacity 1→0.3→1).
- Positioned to the right of the "Tools" label using `margin-left: auto`.
- When all servers are healthy, the dot is not rendered (no green "all good" indicator — absence is the signal).

### 7.2 Data Source

- Reuses `useMcpServers()` hook which fetches `GET /api/v1/mcp/servers`.
- Client-side health check: a server is unhealthy if `last_discovered_at` is null (never successfully discovered).
- Future enhancement: backend could add a `health_status` field from periodic health checks. For now, discovery status is the proxy.

### 7.3 Layout Integration

- Add the health dot inside the Tools `<NavLink>` component in `Layout.tsx`.
- The dot must not appear when the sidebar is collapsed (icon-only mode) — instead, tint the Tools icon itself orange when unhealthy.

---

## 8. Backend Changes Required

### 8.1 Add `model` Column to Runs Table

The Cost by Model chart requires knowing which model was used for each run. Currently the model is only in the policy YAML, not on the run record.

**Migration:**
```sql
ALTER TABLE runs ADD COLUMN model TEXT NOT NULL DEFAULT '';
```

**Writer change:** When creating a new run in the agent runner, copy the model name from the parsed policy into the run record. The model field should store the display-friendly name (e.g., "Sonnet 4", "Haiku 3.5", "Opus 4") not the API model ID.

**API change:** Add `model` to the `RunSummary` JSON response.

### 8.2 New Endpoint: Dashboard Time-Series Data

The charts need hourly-bucketed data that the current `/api/v1/runs` list endpoint doesn't efficiently provide. Add:

**`GET /api/v1/stats/timeseries`**

Query parameters:
- `window` (optional, default: `24h`) — time window. Supported: `24h`, `7d`, `30d`.
- `bucket` (optional, default: `1h`) — bucket size. Supported: `1h`, `6h`, `1d`.

Response:
```json
{
  "buckets": [
    {
      "timestamp": "2026-04-01T14:00:00Z",
      "completed": 5,
      "failed": 0,
      "waiting_for_approval": 1,
      "waiting_for_feedback": 0,
      "cost_by_model": {
        "Sonnet 4": 1240,
        "Haiku 3.5": 380
      }
    }
  ]
}
```

Notes:
- `cost_by_model` values are token counts (not dollars). Dollar conversion happens client-side using a pricing constants file.
- Buckets are aligned to clock hours (e.g., 14:00-15:00, not 14:23-15:23).
- Empty buckets are included (all zeroes) to maintain consistent x-axis spacing.
- SQL implementation: `GROUP BY strftime('%Y-%m-%dT%H:00:00Z', created_at)` with a `CASE WHEN` for status categorization.

### 8.3 Extend Stats Endpoint

Update `GET /api/v1/stats` to include:
- `pending_feedback` (count of feedback_requests with status `pending`) — needed for Zone 2 count badge.

---

## 9. Components to Remove

### 9.1 OnboardingSteps Component

Delete entirely:
- `frontend/src/components/dashboard/OnboardingSteps/OnboardingSteps.tsx`
- `frontend/src/components/dashboard/OnboardingSteps/OnboardingSteps.module.css`
- `frontend/src/components/dashboard/OnboardingSteps/OnboardingSteps.stories.tsx` (if exists)
- `frontend/src/components/dashboard/OnboardingSteps/index.ts` (if exists)
- Remove all imports and references from `DashboardPage.tsx`

### 9.2 StatsBar Component

Delete entirely:
- `frontend/src/components/dashboard/StatsBar/StatsBar.tsx`
- `frontend/src/components/dashboard/StatsBar/StatsBar.module.css`
- `frontend/src/components/dashboard/StatsBar/StatsBar.stories.tsx` (if exists)
- `frontend/src/components/dashboard/StatsBar/index.ts` (if exists)
- Remove all imports and references

### 9.3 StatusBoard Component

Delete entirely:
- `frontend/src/components/dashboard/StatusBoard/StatusBoard.tsx`
- `frontend/src/components/dashboard/StatusBoard/StatusBoard.module.css`
- `frontend/src/components/dashboard/StatusBoard/StatusBoard.stories.tsx` (if exists)
- `frontend/src/components/dashboard/StatusBoard/index.ts` (if exists)
- Remove all imports and references

---

## 10. Components to Create

| Component | Location | Purpose |
|---|---|---|
| `RunActivityChart` | `src/components/dashboard/RunActivityChart/` | Zone 1 left panel — recharts area chart for run outcomes |
| `CostByModelChart` | `src/components/dashboard/CostByModelChart/` | Zone 1 right panel — recharts area chart for model costs |
| `AttentionQueue` | `src/components/dashboard/AttentionQueue/` | Zone 2 — collapsible attention items list |
| `AttentionItem` | `src/components/dashboard/AttentionQueue/AttentionItem.tsx` | Individual attention card (approval/question/failure) |
| `RecentRunsFeed` | `src/components/dashboard/RecentRunsFeed/` | Zone 3 — compact run list |
| `McpHealthDot` | `src/components/Layout/McpHealthDot.tsx` | Sidebar health indicator dot |

Each component gets a `.module.css` file and a `.stories.tsx` file for Storybook.

---

## 11. Components to Keep/Reuse

| Component | Usage |
|---|---|
| `ActivityFeed` | Reference for Zone 3 styling patterns, but replaced by `RecentRunsFeed` with different columns. Delete after Zone 3 is built. |
| `StatusBadge` | Reused in Zone 3 run rows for status display |
| `EmptyState` | Reused for Zone 3 empty state |
| `PageHeader` | Reused for "Control Center" title |

---

## 12. New Hooks and API Integration

### 12.1 `useTimeSeriesStats(window?: string)`

```typescript
// Fetches GET /api/v1/stats/timeseries?window=24h
// Returns { buckets: TimeSeriesBucket[] }
// Query key: ['stats', 'timeseries', window]
// Refetch interval: 60 seconds (charts don't need real-time SSE updates)
```

### 12.2 `useAttentionItems()`

```typescript
// Combines three queries:
//   - runs with status waiting_for_approval
//   - runs with status waiting_for_feedback  
//   - runs with status failed (since 24h ago)
// Merges, sorts by urgency (expires_at ascending)
// Filters out dismissed failures from localStorage
// Returns { items: AttentionItem[], count: number }
```

### 12.3 Model Pricing Constants

```typescript
// frontend/src/constants/pricing.ts
export const MODEL_PRICING: Record<string, { input: number; output: number }> = {
  'Sonnet 4':  { input: 3.00 / 1_000_000, output: 15.00 / 1_000_000 },
  'Haiku 3.5': { input: 0.80 / 1_000_000, output: 4.00 / 1_000_000 },
  'Opus 4':    { input: 15.00 / 1_000_000, output: 75.00 / 1_000_000 },
  // ... add models as needed
}
```

Since we only have combined token count (not input/output split), cost estimation uses a blended rate. The pricing file documents this approximation.

---

## 13. Recharts Theming

Recharts components must match Gleipnir's design system:

- **Font:** `var(--font-mono)` for axis labels and tooltips
- **Axis labels:** 10px, `var(--text-muted)`
- **Grid lines:** `var(--border-subtle)` at 30% opacity, horizontal only
- **Tooltip:** `var(--bg-elevated)` background, `1px solid var(--border-mid)` border, `border-radius: 6px`
- **Cursor line:** `var(--border-mid)` vertical line on hover
- **No animation on initial render** — data should appear immediately, not animate in (avoids the "PowerPoint" anti-pattern)
- **Smooth curve type:** `monotone` for natural-looking lines

---

## 14. Storybook Stories

Each new component needs stories with:
- Default state (with realistic mock data)
- Empty state (zero data)
- Loading state (skeleton)
- Edge cases: single model, many models, all failures, no failures, expired countdown

Zone 2 stories should demonstrate the sort order and color coding.

---

## 15. Testing Strategy

### 15.1 Frontend Unit Tests (Vitest)

- `useAttentionItems`: test merge logic, sort order, dismiss filtering
- `useTimeSeriesStats`: test data transformation and bucket alignment  
- Pricing calculation: test token-to-dollar conversion
- Countdown formatting: test edge cases (expired, < 2min urgent threshold)

### 15.2 Backend Tests (Go)

- Time-series SQL query: test bucketing, empty buckets, status categorization
- `model` field: test it's populated on run creation and included in API response
- Stats endpoint: test `pending_feedback` count

### 15.3 Storybook Visual Tests

- All new components have stories exercising their visual states
- Use existing Storybook + Playwright test infrastructure

---

## 16. Migration Checklist

This is the execution order:

1. **Backend: Migration** — add `model` column to runs table
2. **Backend: Agent runner** — populate `model` on run creation
3. **Backend: API** — add `model` to RunSummary, add `/api/v1/stats/timeseries` endpoint, add `pending_feedback` to stats
4. **Frontend: Install** — add `recharts` dependency
5. **Frontend: Remove** — delete OnboardingSteps, StatsBar, StatusBoard components
6. **Frontend: Rename** — Dashboard → Control Center (sidebar, page title, icon)
7. **Frontend: Zone 1** — RunActivityChart + CostByModelChart components
8. **Frontend: Zone 2** — AttentionQueue + AttentionItem components
9. **Frontend: Zone 3** — RecentRunsFeed component
10. **Frontend: Sidebar** — McpHealthDot component in Layout
11. **Frontend: Wire up** — Rebuild DashboardPage with new zones
12. **Frontend: Stories** — Storybook stories for all new components
13. **Frontend: Tests** — Unit tests for hooks and utilities
14. **Cleanup** — Remove ActivityFeed component (replaced by RecentRunsFeed)
