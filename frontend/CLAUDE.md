## Frontend overview

React + TypeScript UI for Gleipnir. In production, `npm run build` produces `frontend/dist/` which is embedded into the Go binary via `go:embed` in `frontend/embed.go` and served directly by the Go HTTP server. Vite for dev/build, Storybook for component development, Vitest + Playwright for testing.

## Commands

```bash
npm run dev              # Vite dev server (proxies /api → localhost:8080)
npm run build            # TypeScript check + production build
npm run preview          # preview production build
npm run test:unit        # Vitest unit tests (jsdom)
npm run storybook        # Storybook on port 6006
npm run build-storybook  # static Storybook build
```

## Route structure

```
/login              → login page (unauthenticated)
/setup              → initial admin account setup (unauthenticated)
/                   → redirect to /dashboard
/dashboard          → stats bar, policy list grouped by folder
/runs               → paginated run history with filters
/agents             → agent (policy) list
/agents/new         → agent editor (create)
/agents/:id         → agent editor (edit)
/agents/:id/runs    → redirect to /runs?policy=:id
/runs/:id           → reasoning timeline with live SSE updates
/tools              → tool management + server registry
/mcp                → redirect to /tools (legacy path)
/users              → user management (admin)
/admin/models       → model enable/disable, provider key management (admin)
/admin/system       → system settings (public URL, run limits, system info) (admin)
*                   → 404 not found
```

All authenticated routes render inside a shared `Layout` component (sidebar + content area). Each route has its own `errorElement` boundary.

## Design system

Design tokens live in `src/tokens.css`. The system supports dark, light, and system-preference themes via `data-theme` attribute on `:root`.

### Color palette (dark theme defaults)

Nordic Forest palette. `--color-blue` holds warm orange (see tokens.css comment).

```
--bg-canvas:     #141a16      --color-blue:    #d4915a  (tools, running — warm orange)
--bg-surface:    #1b2420      --color-orange:  #e0a26e
--bg-elevated:   #243029      --color-amber:   #f0c830  (approvals)
--bg-sidebar:    #141a16      --color-green:   #4ade6a  (success, complete)
--bg-code:       #121916      --color-red:     #f05545  (errors, failed)
--border-subtle: #3a4840      --color-purple:  #a78bfa  (feedback, interrupted)
--border-mid:    #4d5e54      --color-teal:    #34d399  (poll triggers)
--text-faint:    #3a4840      --color-info:    #40c8e0
--text-muted:    #636e62      --accent-hover:  #e0a26e
--text-second:   #a8a090      --accent-muted:  rgba(212, 145, 90, 0.14)
--text-primary:  #e4ddd2      --bg-subtle:     #2c3832
```

Capability aliases: `--color-tool: var(--color-blue)`, `--color-feedback: var(--color-purple)`.

### Typography

- Body: `IBM Plex Sans, system-ui, sans-serif` (`--font-body`)
- Mono: `IBM Plex Mono, monospace` (`--font-mono`)
- Scale: 11 / 13 / 15 / 18 / 24 / 32 px (`--text-xs` through `--text-2xl`)
- Weights: 300 (light), 400 (normal), 500 (medium), 600 (semibold), 700 (bold, wordmark only)

### Spacing

4px base grid via CSS custom properties (`--space-1` through `--space-16`, plus `--space-20` and `--space-32` for larger fixed sizes). All margins, padding, and gaps snap to multiples of 4px. Section radii use `--radius-section`.

### Motion

```
--duration-fast:    120ms   (hover, color)
--duration-normal:  200ms   (expand, slide)
--duration-slow:    350ms   (page transitions)
--ease-out:    cubic-bezier(0.16, 1, 0.3, 1)
--ease-in:     cubic-bezier(0.5, 0, 0.75, 0)
--ease-spring: cubic-bezier(0.34, 1.56, 0.64, 1)
```

Reduced-motion media query sets all durations to 0ms.

### Layout tokens

Sidebar: `--sidebar-width: 232px`, `--sidebar-collapsed: 48px`.

## Import paths

Use the `@/` alias for all imports from `src/`. Prefer `@/tokens.css`, `@/components/Foo`, `@/hooks/useBar` over relative `../../` paths.

This alias is configured in `tsconfig.json` (`paths`) and `vite.config.ts` (`resolve.alias`).

## Styling rules

- **CSS Modules only** — no inline `style={}` attributes. Components get `ComponentName.module.css`.
- CSS Modules consume CSS custom properties defined in `src/tokens.css`.
- Shared utility styles live in `src/styles/` (table, forms, alerts, spinner, badges modules) and are imported directly by components that need them.
- Storybook stories may have their own `.stories.module.css` for layout wrappers.

## State management

- **Server state:** TanStack Query. All query keys are centralized in `src/hooks/queryKeys.ts`.
- **UI state:** React `useState`/`useReducer`, local to owning component. Lift only when siblings share state.
- **No global store** (no Redux/Zustand) unless a clear need emerges.

### Query key families

```typescript
queryKeys.policies.all                  // ['policies']
queryKeys.policies.detail(id)           // ['policies', id]
queryKeys.policies.webhookSecret(id)    // ['policies', id, 'webhook-secret']
queryKeys.runs.all              // ['runs']
queryKeys.runs.detail(id)       // ['runs', id]
queryKeys.runs.steps(id)        // ['runs', id, 'steps']
queryKeys.runs.list(params)     // ['runs', 'list', params]
queryKeys.servers.all           // ['servers']
queryKeys.servers.tools(id)     // ['servers', id, 'tools']
queryKeys.stats.all             // ['stats']
queryKeys.approvals.all         // ['approvals']
queryKeys.users.all             // ['users']
queryKeys.currentUser.all       // ['currentUser']
queryKeys.models.all            // ['models']
queryKeys.config.all            // ['config']
```

### Data fetching

All API calls go through a shared `apiFetch<T>(path, init?)` wrapper (`src/api/fetch.ts`) that unwraps `{ data: T }` envelopes and throws typed `ApiError` on failure. TanStack Query hooks wrap `apiFetch`.

### SSE integration

A single `useSSE` hook at the root layout connects to `GET /api/v1/events`. On event arrival, it invalidates relevant TanStack Query caches. For high-frequency `run.step_added` events, use optimistic cache updates instead of refetching.

Event types: `run.status_changed`, `run.step_added`, `approval.created`, `approval.resolved`.

## Formatting utilities

Canonical formatting helpers live in `src/utils/format.ts`:

- `formatDuration(s)` — seconds to human-readable (`42s`, `3m 12s`)
- `formatDurationMs(ms)` — milliseconds variant
- `formatTokens(n)` — token counts (`1.2k`, `3.5M`)
- `formatTimestamp(iso)` — absolute timestamp (`Apr 1, 14:30`)
- `formatTimeAgo(iso)` — relative time (`5m ago`, `2h ago`)
- `formatCountdown(expiresAt)` — countdown with urgency flag
- `computeRunDuration(run)` — derives duration from started/completed timestamps
- `formatProviderName(provider)` — display label for LLM providers (`openai` → `OpenAI`)

## API types

All API response types are defined in `src/api/types.ts`, with comments mapping each type to its Go backend counterpart (e.g., `ApiRun` matches `trigger/runs_handler.go → RunSummary`).

## API surface

```
Auth:
POST   /api/v1/auth/setup              POST   /api/v1/auth/login
POST   /api/v1/auth/logout             GET    /api/v1/auth/status

Policies:
GET    /api/v1/policies                POST   /api/v1/policies
GET    /api/v1/policies/:id            PUT    /api/v1/policies/:id
DELETE /api/v1/policies/:id
GET    /api/v1/policies/:id/webhook/secret   (admin|operator)
POST   /api/v1/policies/:id/webhook/rotate   (admin|operator)

Runs:
GET    /api/v1/runs                    GET    /api/v1/runs/:id
GET    /api/v1/runs/:id/steps          POST   /api/v1/runs/:id/cancel

MCP / Tools:
GET    /api/v1/mcp/servers             POST   /api/v1/mcp/servers
POST   /api/v1/mcp/servers/test
DELETE /api/v1/mcp/servers/:id
POST   /api/v1/mcp/servers/:id/discover
GET    /api/v1/mcp/servers/:id/tools

Approvals:
GET    /api/v1/approvals
POST   /api/v1/approvals/:id/approve
POST   /api/v1/approvals/:id/reject

Users:
GET    /api/v1/users                   POST   /api/v1/users
PUT    /api/v1/users/:id

Models:
GET    /api/v1/models

Triggers + Events:
POST   /api/v1/webhooks/:policyID      (trigger endpoint)
GET    /api/v1/events                   (SSE stream)
GET    /api/v1/health
```

Response envelope: `{ data: T }` for success, `{ error: string, detail?: string }` for failure.

## Component structure

### Pages (`src/pages/`)

10 page components, each with a corresponding `.module.css`. Pages are thin wrappers that compose hooks and components.

### Components (`src/components/`)

Organized by feature area:

- **Layout** — sidebar navigation, content area, theme toggle, connection status banner
- **dashboard/** — StatsBar, StatusBadge, StatusBoard, TriggerChip, ActivityFeed, OnboardingSteps
- **AgentEditor/** — the agent editor (EditorTopBar, FormMode with 7 form sections)
- **AgentList/** — agent list with folder grouping
- **RunDetail/** — RunHeader, StepTimeline, FilterBar, MetadataGrid, CapabilitySnapshotCard, ThoughtBlock, ThinkingBlock, ToolBlock, CompleteBlock, ErrorBlock, FeedbackBlock, ApprovalActions, FeedbackActions
- **MCPPage/** — ServerCard, ToolList, ToolRow, MCPStatsBar, HealthIndicator, AddServerModal, DeleteServerModal
- **Shared** — Button, Modal, ModalFooter, EmptyState, ErrorBoundary, QueryBoundary, CopyBlock, CollapsibleJSON, SkeletonBlock, PageHeader, ApprovalBanner, ConnectionBanner, TriggerRunModal

### Hooks (`src/hooks/`)

~28 custom hooks. Data-fetching hooks follow the pattern: `use{Resource}` wraps a TanStack `useQuery`, `use{Action}` wraps a `useMutation`. All query keys go through `queryKeys.ts`.

### Storybook

45 story files covering components, dashboard widgets, form sections, and hook demonstrations. Stories use MSW for API mocking and have their own `.stories.module.css` for layout scaffolding where needed.

## Key architectural decisions

- **ADR-016:** SSE for real-time transport, not WebSockets. The Go SSE handler sets `X-Accel-Buffering: no` directly for compatibility with upstream reverse proxies.
- **ADR-019:** Agent editor (originally dual-mode policy editor). Form view is the only editing surface; YAML is the API payload. YAML tab was removed in #751.
- **ADR-020:** Policy folders are a YAML-only `folder` field for UI grouping. No DB column.
- **ADR-030:** UI abstracts over tool transport — the Tools page is protocol-agnostic (not "MCP page").
- **Hard capability enforcement:** disallowed tools are never registered with the agent. The UI displays what the runtime enforces.

## Testing

- **Unit tests:** Vitest with jsdom, `npm run test:unit`. Tests live alongside components (`*.test.tsx`).
- **Component stories:** Storybook with `@storybook/addon-vitest` for story-level assertions.
- **API mocking:** MSW (Mock Service Worker) for both tests and Storybook stories.
- **Browser tests:** Playwright via `@vitest/browser-playwright` (not yet widely used).
