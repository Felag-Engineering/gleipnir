# Run History Page Redesign

**Date:** 2026-04-02
**Status:** Approved
**Scope:** Runs list page (`/runs`) — visual redesign only, no backend changes

## Goal

Redesign the Runs list page to feel intentionally crafted rather than generically generated. The page is functionally solid but lacks visual identity. The redesign applies restraint, clear hierarchy, and Gleipnir-specific design language to elevate it from "default data table" to something that feels loved.

## Design Principles Applied

- **Restraint over decoration** — every visual element earns its place
- **Strong visual hierarchy** — policy name dominates, metadata recedes
- **Color with purpose** — status stripe and badge tints encode meaning, not decoration
- **No header row** — data is self-evident through typography and position; dropping the header is the strongest anti-generic move
- **Filter-responsive stats** — the inline stats subtitle updates to reflect the current filter, making it functional rather than decorative

## Page Rename

- Page title: **"Run History"** (was "Runs")
- Sidebar nav label: **"Run History"** (was "Runs")
- Route remains `/runs` and `/runs/:id` — no URL change
- `usePageTitle` updates to "Run History"

## Layout (top to bottom)

### 1. Title Row

```
Run History                                    94.3% success · 842k tokens (24h)
```

- Left: `Run History` at `--text-xl` (24px), `--weight-bold`
- Right: Contextual stats in `--font-mono`, `--text-sm`, `--text-muted`
- Stats reflect the active filter state and date range
- Stats format: `{success_rate}% success · {total_tokens} tokens ({range_label})`
- Success rate = completed / (completed + failed) as percentage, excluding running/pending/interrupted
- When no runs match filters, stats line reads: `No runs`

### 2. Filter Bar

Horizontal row of chip-style filters replacing the three `<select>` dropdowns.

**Status chips** (left group):
- `All {count}` | `Complete {count}` | `Running {count}` | `Failed {count}` | `Approval {count}`
- Count rendered as a small badge pill inside each chip
- Active chip: `background: rgba(--color-blue, 0.15)`, `border: 1px solid rgba(--color-blue, 0.25)`, `color: --color-blue`, `font-weight: --weight-medium`
- Inactive chip: `border: 1px solid --border-mid`, `color: --text-second`
- Chip border-radius: 14px
- Chip padding: 5px 12px
- Font size: `--text-sm` (13px) — note: mockups showed 12px but we snap to the design system scale
- Count badge: `font-size: --text-xs`, `padding: 1px 6px`, `border-radius: 8px`, `background: rgba(--color-blue, 0.2)` for active, `rgba(255,255,255,0.05)` for inactive

**Vertical divider** between status chips and secondary filters:
- `width: 1px`, `height: 20px`, `background: --border-mid`, `margin: 0 var(--space-1)`

**Secondary filters** (right of divider):
- Policy: `All policies` chip with `▾` suffix — opens a dropdown/popover of policy names on click
- Date range: `Last 24h` chip with `▾` suffix — opens dropdown with options: All time, Last hour, Last 24h, Last 7 days
- Both use the same inactive chip styling as status chips
- When a non-default value is selected, the chip text updates and gets a subtle active treatment (same as status active style)

**Sort control** (right-aligned, pushed with `margin-left: auto`):
- `Newest ▼` chip — toggles between `Newest ▼` and `Oldest ▲` on click
- Styled smaller: `font-size: --text-xs`, `padding: 4px 10px`, `border-radius: 12px`
- `color: --text-muted`, `border: 1px solid --border-subtle`

**Filter bar layout:**
- `display: flex`, `gap: var(--space-2)` (6px between chips), `flex-wrap: wrap`, `align-items: center`
- `margin-bottom: var(--space-4)`

### 3. Run Rows

Each run is a single flex row with no enclosing table/grid container. No header row.

**Row layout:**
```
[stripe] [policy name + subtext] ... [status badge] [duration] [time + tokens] [arrow]
```

**Dimensions and spacing:**
- Row padding: `10px 12px` (`var(--space-3)` vertical, `var(--space-3)` horizontal)
- Row gap: 2px between rows
- Row border-radius: `0 4px 4px 0` (flat left edge for stripe)
- `display: flex`, `align-items: center`, `gap: var(--space-3)`

**Left status stripe:**
- `width: 3px`, `align-self: stretch`, `border-radius: 2px`
- Colors by status:
  - complete: `--color-green` (#4ade80)
  - running: `--color-blue` (#60a5fa)
  - failed: `--color-red` (#f87171)
  - waiting_for_approval: `--color-amber` (#f59e0b)
  - interrupted: `--color-purple` (#a78bfa)
  - pending: `--text-faint` (#334155)

**Identity zone** (flex: 1):
- Policy name: `--text-base` (15px — using design system scale, not mockup 14px), `--weight-medium`, `--text-primary`, `white-space: nowrap`, `overflow: hidden`, `text-overflow: ellipsis`
- Subtext: `--text-xs` (11px), `--font-mono`, `--text-faint`, `margin-top: 1px`
- Subtext content: `{run_id first 8 chars} · {trigger_type}`

**Status badge:**
- Existing `StatusBadge` component — already has the correct pill styling with tinted backgrounds
- No changes needed to the component itself; it already renders the right colors and animations

**Duration column:**
- `--text-sm` (13px), `--font-mono`, `--text-muted`
- `min-width: 52px`, `text-align: right`
- Shows `formatDuration()` output, or `—` for in-progress/pending runs

**Time + tokens cluster** (right-aligned, stacked):
- Container: `min-width: 72px`, `text-align: right`
- Top line: `formatTimeAgo()` in `--text-sm`, `--font-mono`, `--text-second`
- Bottom line: `{formatTokens()} tok` in `--text-xs`, `--font-mono`, `--text-muted`, `margin-top: 1px`

**Arrow indicator:**
- `›` character, `--text-faint` color, `width: 16px`, `text-align: center`
- On row hover, transitions to `--text-second`

**Row backgrounds by status:**
- complete/pending: `rgba(--bg-elevated, 0.3)` — neutral
- running: `rgba(--color-blue, 0.04)`
- failed: `rgba(--color-red, 0.04)`
- waiting_for_approval: `rgba(--color-amber, 0.04)`
- interrupted: `rgba(--color-purple, 0.04)`

**Row hover:**
- Background shifts to next elevation: `var(--bg-elevated)`
- Arrow `›` color transitions to `--text-second`
- Transition: `background var(--duration-fast) var(--ease-out)`

**Row click:**
- Entire row is a `<Link to={/runs/${run.id}}>` (preserves current behavior)

### 4. Pagination

**Layout:** `display: flex`, `justify-content: space-between`, `align-items: center`
- Top border: `1px solid --border-subtle`
- `margin-top: var(--space-4)`, `padding-top: var(--space-3)`

**Left side:** `1–25 of 247` in `--text-sm`, `--text-muted`

**Right side:** Page number buttons
- Each button: `padding: 4px 10px`, `border-radius: 4px`, `font-size: --text-sm`
- Active page: `background: rgba(--color-blue, 0.15)`, `border: 1px solid rgba(--color-blue, 0.25)`, `color: --text-primary`
- Inactive page: `border: 1px solid --border-subtle`, `color: --text-second`
- Arrow buttons: `←` and `→` with same inactive styling, disabled when at bounds
- Ellipsis: `…` in `--text-muted`, no button styling
- Show: first page, current window (2-3 pages around current), last page, with ellipsis for gaps
- `gap: 4px` between buttons

### 5. Empty State

When no runs match filters:
- Reuse existing `EmptyState` component pattern
- Headline: "No runs found"
- Subtext: "Try adjusting the filters, or go to Policies to trigger a run."
- Link to `/policies`

## Stats Computation

The inline stats (`94.3% success · 842k tokens`) are computed client-side from the current query response:

- **Success rate:** `completed_count / (completed_count + failed_count) * 100` — only considers terminal states, ignores running/pending/interrupted
- **Total tokens:** Sum of `token_cost` across all runs matching the current filter
- Both values come from the existing `/api/v1/runs` response which returns `total` count. However, for success rate and token sum we need counts per status.

**Backend requirement:** The existing `GET /api/v1/runs` endpoint returns `{ runs: [...], total: number }`. To power the stats without a separate request, add an optional `stats` field to the response:

```json
{
  "runs": [...],
  "total": 247,
  "stats": {
    "completed": 232,
    "failed": 8,
    "running": 2,
    "pending": 0,
    "waiting_for_approval": 5,
    "interrupted": 0,
    "total_tokens": 842000
  }
}
```

This is computed by the same query that fetches the runs, so it respects the active date range and policy filters. The `stats` field is always included (no opt-in parameter needed).

**If backend change is deferred:** The stats subtitle can initially show only `{total} runs` without success rate or token totals, using the already-available `total` field. The full stats can be added when the backend endpoint is updated. The component should handle `stats` being undefined gracefully.

## Components Changed

| Component | Change |
|---|---|
| `RunsPage.tsx` | Full rewrite — new layout, filter chips, row markup, pagination, stats |
| `RunsPage.module.css` | Full rewrite — new styles for all sections |
| `Layout.tsx` | Rename sidebar nav label from "Runs" to "Run History" |
| `Layout.module.css` | No changes expected |
| `PageHeader` | May no longer be used if title is custom-rendered with stats |

## Components NOT Changed

- `StatusBadge` — reused as-is inside rows
- `TriggerChip` — dropped (trigger type shown as plain text in subtext line)
- `RunDetailPage` — out of scope
- `Button` — pagination buttons are custom-styled, not using the shared Button component
- Backend routes/handlers — URL stays `/runs`

## New Sub-Components (Optional Extraction)

If the page file grows beyond ~200 lines, extract:
- `RunFilterBar` — chip filters, policy/date dropdowns, sort control
- `RunRow` — single row rendering with stripe, identity, badge, timing
- `RunPagination` — page number buttons with ellipsis logic

These are only needed for file size management — no architectural reason to extract prematurely.

## Accessibility

- Filter chips: `role="radio"` within a `role="radiogroup"` for status filters
- Policy/date chips: standard `<select>` hidden behind styled chip, or `<button>` with `aria-haspopup="listbox"`
- Sort control: `<button>` with `aria-label="Sort by date, currently newest first"`
- Row links: Already `<Link>` elements (focusable, keyboard navigable)
- Arrow indicator `›`: `aria-hidden="true"` (decorative)
- Page buttons: `aria-label="Page {n}"`, `aria-current="page"` on active
- Status stripe: decorative (`aria-hidden="true"`), status communicated via badge text

## Responsive Behavior

- Below 768px: Filter chips wrap to second line, sort control drops below
- Below 480px: Duration column hides, time+tokens cluster stacks more aggressively
- Sidebar auto-collapses per existing Layout behavior

## Test Strategy

- **Unit tests:** Filter chip selection updates URL params, pagination math, stats computation (success rate with edge cases: 0 runs, all failed, no terminal runs)
- **Component rendering:** Rows render correct stripe color, badge, subtext for each status
- **Storybook stories:** RunsPage with various data states (empty, single page, multi-page, mixed statuses, all one status)
- **Existing tests:** Update Layout tests if sidebar nav label assertion changes from "Runs" to "Run History"
