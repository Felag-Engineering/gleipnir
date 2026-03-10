## Frontend overview

React + TypeScript UI for Gleipnir. Served by nginx in production, which proxies `/api` to the Go backend. Vite for dev/build, Storybook for component development, Vitest + Playwright for testing.

## Commands

```bash
npm run dev              # Vite dev server (proxies /api → localhost:8080)
npm run build            # TypeScript check + production build
npm run preview          # preview production build
npm run storybook        # Storybook on port 6006
npm run build-storybook  # static Storybook build
```

## Design reference

Visual language and interaction patterns are defined in `docs/frontend_mockups/`:

- `gleipnir-dashboard.jsx` — dashboard layout, stats bar, policy list, folder grouping
- `gleipnir-policy-editor.jsx` — dual-mode YAML/form editor, tool picker
- `gleipnir-reasoning-timeline.jsx` — run detail view, step timeline, filter chips
- `gleipnir-mcp-registry.jsx` — MCP server management, tool list, capability roles

These are JSX reference mockups (not runnable components). Use them as the authoritative source for layout, spacing, color usage, and interaction behavior when building real components.

## Route structure

```
/                   → redirect to /dashboard
/dashboard          → stats bar, policy list grouped by folder
/policies/new       → dual-mode policy editor (create)
/policies/:id       → dual-mode policy editor (edit)
/runs/:id           → reasoning timeline with live SSE updates
/mcp                → MCP server management + tool registry
```

## Design system

### Color palette (dark theme)

```
--bg-canvas:     #0F1117      --color-blue:    #60A5FA  (sensors, running)
--bg-surface:    #131720      --color-orange:  #FB923C  (actuators)
--bg-elevated:   #1E2330      --color-amber:   #F59E0B  (approvals)
--bg-topbar:     #0D1018      --color-green:   #4ADE80  (success, complete)
--bg-code:       #090C12      --color-red:     #F87171  (errors, failed)
--border-subtle: #1E2330      --color-purple:  #A78BFA  (feedback, interrupted)
--border-mid:    #253044      --color-teal:    #34D399  (poll triggers)
--text-faint:    #334155
--text-muted:    #475569
--text-second:   #94A3B8
--text-primary:  #E2E8F0
```

### Typography

- Body: `IBM Plex Sans, system-ui, sans-serif`
- Mono: `IBM Plex Mono, monospace`
- Scale: 11 / 13 / 15 / 18 / 24 / 32 px
- Weights: 400 (normal), 500 (medium), 600 (semibold), 700 (GLEIPNIR wordmark only)

### Spacing

4px base grid. All margins, padding, and gaps snap to: 4, 8, 12, 16, 24, 32, 48, 64 px.

### Motion

```
--duration-fast:    120ms   (hover, color)
--duration-normal:  200ms   (expand, slide)
--duration-slow:    350ms   (page transitions)
--ease-out:    cubic-bezier(0.16, 1, 0.3, 1)
--ease-in:     cubic-bezier(0.5, 0, 0.75, 0)
--ease-spring: cubic-bezier(0.34, 1.56, 0.64, 1)
```

## Styling rules

- **CSS Modules only** — no inline `style={}` attributes. Components get `ComponentName.module.css`.
- CSS Modules consume CSS custom properties defined in a global stylesheet.
- Existing components in `src/components/dashboard/` use inline styles and need migration.

## State management

- **Server state:** TanStack Query. Query key families: `['policies']`, `['runs', runId]`, `['servers', serverId, 'tools']`, etc.
- **UI state:** React `useState`/`useReducer`, local to owning component. Lift only when siblings share state.
- **No global store** (no Redux/Zustand) unless a clear need emerges.

### Data fetching

All API calls go through a shared `apiFetch<T>(path, init?)` wrapper that unwraps `{ data: T }` envelopes and throws typed `ApiError` on failure. TanStack Query hooks wrap `apiFetch`.

### SSE integration

A single `useSSE` hook at the root layout connects to `GET /api/v1/events`. On event arrival, it invalidates relevant TanStack Query caches. For high-frequency `run.step_added` events, use optimistic cache updates instead of refetching.

Event types: `run.status_changed`, `run.step_added`, `approval.created`, `approval.resolved`.

## API surface

```
GET    /api/v1/policies                 POST   /api/v1/policies
GET    /api/v1/policies/:id             PUT    /api/v1/policies/:id
DELETE /api/v1/policies/:id

GET    /api/v1/runs                     GET    /api/v1/runs/:id
GET    /api/v1/runs/:id/steps           POST   /api/v1/runs/:id/cancel

GET    /api/v1/mcp/servers              POST   /api/v1/mcp/servers
DELETE /api/v1/mcp/servers/:id
POST   /api/v1/mcp/servers/:id/discover
GET    /api/v1/mcp/servers/:id/tools    PATCH  /api/v1/mcp/tools/:id

GET    /api/v1/approvals                (v0.2)
POST   /api/v1/approvals/:id/approve    (v0.2)
POST   /api/v1/approvals/:id/reject     (v0.2)

POST   /api/v1/webhooks/:policyID       (trigger endpoint)
GET    /api/v1/events                    (SSE stream)
GET    /api/v1/health
```

Response envelope: `{ data: T }` for success, `{ error: string, detail?: string }` for failure.

## Existing components

Components in `src/components/dashboard/` are Storybook-ready prototypes:

- **StatusBadge** — colored dot + label for run status (complete/running/waiting/failed/interrupted)
- **TriggerChip** — monospace label for trigger type (webhook=blue, cron=purple, poll=teal)
- **StatsBar** — 4-card grid (active runs, pending approvals, policies, tokens today)
- **ApprovalCard** — approval request with countdown, reasoning toggle, approve/reject actions
- **ReasoningTrace** — vertical timeline of thought/tool_call/tool_result steps

Types in `types.ts`, fixtures in `fixtures.ts`, helpers in `styles.ts` (`fmtDur`, `fmtTok`, `fmtAbs`, `fmtRel`, `timeLeft`).

## Key architectural decisions

- **ADR-016:** SSE for real-time transport, not WebSockets. nginx must set `X-Accel-Buffering: no` on SSE responses.
- **ADR-019:** Dual-mode policy editor. Form view + YAML view edit the same YAML string. YAML is the API payload.
- **ADR-020:** Policy folders are a YAML-only `folder` field for UI grouping. No DB column.
- **Hard capability enforcement:** disallowed tools are never registered with the agent. The UI displays what the runtime enforces.

## Implementation phases (v0.1)

1. **Scaffolding** — global CSS tokens, TanStack Query provider, `apiFetch`, `useSSE`, root layout, error boundaries
2. **Dashboard** — stats bar, policy list with folder grouping, skeleton screens, empty states
3. **Policy editor** — CodeMirror 6 YAML editor, form mode, tool picker, bidirectional sync
4. **Run detail** — reasoning timeline, filter chips, step pagination, live SSE updates
5. **MCP management** — server list, add modal, tool list, capability role dropdown, discovery

See `docs/Frontend_Roadmap.md` for full details on each phase.
