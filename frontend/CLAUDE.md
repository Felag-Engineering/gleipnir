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
/users              → redirect to /admin/users (legacy path)
/admin/users        → user management (admin)
/admin/models       → model enable/disable, provider key management (admin)
/admin/system       → system settings (public URL, run limits, system info) (admin)
/admin/audiences    → audience list (admin|operator: + new; auditor: read-only)
/admin/audiences/new → create audience (admin|operator)
/admin/audiences/:id → audience detail/editor (admin|operator: edit; auditor: read-only)
/admin/plugins      → plugin instance list (admin); shows pending-review section at top (fresh installs require approval before any instances can be created — #242); groups instances pending re-authorization below (#230)
/admin/plugins/:id/review → plugin review consent surface (admin); shows full manifest metadata (services, auth strategy, pubkey fingerprint, SBOM) with Approve/Reject actions (#242)
/admin/plugins/:id/instances/:iid → plugin instance editor (admin); Subscriptions tab edits coarse watch scope (#223), Credentials tab renders per the manifest's auth strategy (write-only static API key / header set / basic auth; Authorize / Re-authorize for OAuth — #231), Config tab renders the instance-level `config_schema` from `usePluginInstanceDetail` (NOT the listing's channel schema) with secret fields write-only/redacted (#241, fixed in #602)
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
- `--text-2xs: 10px` — micro type for badges/chips that sit below the `--text-xs` (11px) floor. Use only for dense metadata labels; prefer `--text-xs` for normal small text. Never hardcode `font-size: 10px`.
- Weights: 300 (light), 400 (normal), 500 (medium), 600 (semibold), 700 (bold, wordmark only)

### Spacing

4px base grid via CSS custom properties (`--space-1` through `--space-16`, plus `--space-20` and `--space-32` for larger fixed sizes). All margins, padding, and gaps snap to multiples of 4px.

### Radii

Small-element `border-radius` values come from the radius scale — never hardcode `2/3/4/6px`:

```
--radius-xs:       2px   (thin bars, tiny chips)
--radius-sm:       3px   (small tags/chips)
--radius-md:       4px   (default — inputs, buttons, cards)
--radius-lg:       6px   (panels, larger cards)
--radius-section:  8px   (page sections)
```

Larger structural radii (modals/pills at 10/12/14/16/999px) are intentionally left as raw values for now — they are distinct one-offs, not part of the small-element drift the scale addresses; a future pass may fold them in.

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
queryKeys.servers.tools(id)     // ['servers', id, 'tools']        — enabled tools only (legacy; kept for cache-invalidation compatibility)
queryKeys.servers.toolsAll(id)  // ['servers', id, 'tools', 'all'] — all tools incl. disabled (used by Tools page and policy form)
queryKeys.stats.all             // ['stats']
queryKeys.approvals.all         // ['approvals']
queryKeys.users.all             // ['users']
queryKeys.currentUser.all       // ['currentUser']
queryKeys.models.all            // ['models']
queryKeys.config.all            // ['config']
queryKeys.plugins.list          // ['plugins', 'list']               — installed plugins (invalidated on plugin install, approve, reject)
queryKeys.plugins.detail(id)    // ['plugins', id, 'detail']          — plugin detail with parsed manifest for review consent surface
queryKeys.plugins.rss           // ['admin', 'plugins', 'rss']        — aggregate plugin process RSS (30s refetch)
queryKeys.plugins.instances(id) // ['plugins', id, 'instances']      — instances of one plugin (invalidated on instance create)
queryKeys.plugins.credentials(p, i) // ['plugins', p, 'instances', i, 'credentials'] — redacted credentials view
queryKeys.plugins.options(p, i, src, q, cur) // per-query cache for AsyncCombobox (pluginId, instanceId, source, query, cursor)
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

`src/utils/inlineMarkdown.ts` — `renderInlineMarkdown(text)` tokenizes bold (`**`), italic (`*`/`_`), and inline code (`` ` ``) and returns a `ReactNode[]` for use inside JSX. No external markdown dependency.

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
PUT    /api/v1/mcp/servers/:id         (admin|operator; name + url only)
PUT    /api/v1/mcp/servers/:id/headers/:name   (admin|operator; set/replace one header)
DELETE /api/v1/mcp/servers/:id/headers/:name   (admin|operator; remove one header)
POST   /api/v1/mcp/servers/test
DELETE /api/v1/mcp/servers/:id
POST   /api/v1/mcp/servers/:id/discover
GET    /api/v1/mcp/servers/:id/tools           (?include_disabled=true for admin/operator)
PUT    /api/v1/mcp/servers/:id/tools/:toolID/enabled
POST   /api/v1/mcp/servers/:id/arcade/authorize        (admin|operator; pre-authorize a toolkit)
POST   /api/v1/mcp/servers/:id/arcade/authorize/wait   (admin|operator; long-poll auth status)

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

Admin / Plugins:
GET    /api/v1/admin/plugin-instances                                         (admin|operator|auditor; list with channel + event_kinds enrichment)
POST   /api/v1/admin/plugins                                                   (admin; install tarball as application/octet-stream, max 100 MiB)
POST   /api/v1/admin/plugins/:id/instances                                     (admin; create instance with {instance_name})
DELETE /api/v1/admin/plugins/:id                                               (admin; uninstall plugin — 409 on policy/audience references, removes bundle dir)
DELETE /api/v1/admin/plugins/:id/instances/:iid                                (admin; delete a single instance — same 409 guards, scoped to one)
GET    /api/v1/admin/plugins/:id/instances/:iid                                (admin; per-instance health detail)
PUT    /api/v1/admin/plugins/:id/instances/:iid/config                         (admin; CAS-guarded instance config blob; validates against manifest config_schema when declared)
PUT    /api/v1/admin/plugins/:id/instances/:iid/credentials/oauth-token        (admin; advanced seed for oauth2_* strategies — escape hatch for E2E/manual recovery)
PUT    /api/v1/admin/settings/default-model                                    (admin; set system default LLM model {provider, name}; 400 on missing key for known providers, 422 on disabled model)
GET    /api/v1/admin/plugins/:id/instances/:iid/options/:source                (admin; dynamic options from plugin ConfigOptionsService; ?query=&cursor=; {data:{options,next_cursor,degraded?}})
```

Response envelope: `{ data: T }` for success, `{ error: string, detail?: string }` for failure.

## Component structure

### Pages (`src/pages/`)

10 page components, each with a corresponding `.module.css`. Pages are thin wrappers that compose hooks and components.

### Components (`src/components/`)

Organized by feature area:

- **Layout** — sidebar navigation, content area, theme toggle, connection status banner; a Contact nav item is pinned to the bottom of the sidebar nav (below the main/admin items) and opens the `ContactTray` (#153)
- **ContactTray** — maintainer contact tray (email mailto + intent-routed GitHub links: "Report a bug" → Issues, "Ask a question" → Discussions). Reuses the shared `Modal` primitive for focus-trap / Esc / outside-click / focus-restore. Triggered from the Layout sidebar bottom; available to all authenticated roles (#153)
- **dashboard/** — StatsBar, StatusBadge, StatusBoard, TriggerChip, ActivityFeed, SetupChecklist (context-aware onboarding checklist that renders until all setup steps are complete)
- **AgentEditor/** — the agent editor (EditorTopBar, FormMode with 8 form sections)
- **AgentList/** — agent list with folder grouping
- **RunDetail/** — RunHeader, StepTimeline, FilterBar, MetadataGrid, CapabilitySnapshotCard, ThoughtBlock, ThinkingBlock, ToolBlock, CompleteBlock, ErrorBlock, FeedbackBlock, ApprovalActions, FeedbackActions
- **MCPPage/** — ServerCard, ToolList, ToolRow, MCPStatsBar, HealthIndicator, AddServerModal, DeleteServerModal, ServerDetailModal (per-header auth editor: existing name fields are read-only, value field is empty with placeholder; save fans out via `useSetMcpServerHeader`/`useDeleteMcpServerHeader`; no sentinel; see ADR-039), ArcadeAuthSection (toolkit-level OAuth pre-authorization for Arcade gateways; renders only when `server.is_arcade_gateway && canManage`; see ADR-040)
- **admin/** — EncryptionKeyNotice (persistent warning banner on the Models page about encryption key backup requirements), PluginHealthChip (colored chip — green/yellow/red/gray — for the 11 plugin-instance health states including `inactive`; pairs with `utils/pluginHealth.ts` for the worst-across-instances aggregate), PluginCard (clickable card for the `/admin/plugins` two-pane list: name, version, service badges Tool/Trigger/Channel, instance count, aggregate health chip; `isSelected` prop for visual selection state), PluginMemoryBar (aggregate plugin RSS display in the plugins page header; shows total bytes + instance count; click-to-expand per-instance breakdown table sorted by RSS descending; 30s polling via `usePluginRSS`), PluginReviewCard (consent surface for pending-review plugins: services, tier-2 capabilities, auth strategy, pubkey fingerprint, SBOM badge, author/license; Approve/Reject buttons), RejectPluginModal (confirmation modal before rejecting a pending-review plugin; follows UninstallPluginModal pattern), InstallPluginButton (file-picker upload of a plugin tarball; persistent success card with "Review & approve" link for `pending_review` status or "Add instance" CTA for `active` status), AddInstanceModal (form to instantiate an installed plugin by name; reused as a standalone modal on the plugins page AND inline from the install-success card; client-side validation + per-status error mapping)
- **form/** — FieldError (inline message under a field), ErrorBanner (top-of-form bulleted summary with scroll-to-field), AsyncCombobox (searchable single- or multi-select dropdown backed by plugin ConfigOptionsService; multi mode renders removable chips; degrades to plain text input when `degraded=true`; controlled via `value`/`onChange`/`onSearch`). SchemaForm accepts an optional `optionsContext` prop (`{search, degraded}`) that wires AsyncCombobox for any property annotated with `x-gleipnir-options` in the JSON Schema; it renders humanized labels (`prop.title ?? humanize(snake_case)`) and a `*` required marker (via CSS `::after`, keeping DOM text clean for `getByLabelText`) from the schema root `required[]` (#627). Shared primitives for surfacing validation/save errors. **The audience editor's per-entry config now renders through SchemaForm (not RJSF — `@rjsf/*` was removed in #627); the array-of-objects `response_buttons` field uses the dedicated `AudienceEditor/ResponseButtonsEditor` (omits the key when empty so the backend Approve/Reject default applies).**
- **Shared** — Button, Modal, ModalFooter, ContactTray, EmptyState, ErrorBoundary, QueryBoundary, CopyBlock, CollapsibleJSON, SkeletonBlock, PageHeader, ApprovalBanner, ConnectionBanner, TriggerRunModal, Toast (app-wide success/error feedback: `ToastProvider` mounted in `App.tsx` above the router, `useToast()` hook for firing `success`/`error`/`info` toasts, `ToastRegion` mounted in `Layout`; auto-dismiss 4s success / 6s error, `duration:0` sticky; stack capped at 3 oldest-evicted + identical toasts deduped; context-based, no global store. Convention: fire a success toast only when the UI has no other confirmation — actions that change a visible badge/toggle in place (pause/resume, tool enable/disable, user activate/deactivate) get NO success toast, only an error toast; `Agent saved`/`Agent deleted`/`User created` keep theirs)

### Hooks (`src/hooks/`)

~29 custom hooks. Data-fetching hooks follow the pattern: `use{Resource}` wraps a TanStack `useQuery`, `use{Action}` wraps a `useMutation`. All query keys go through `queryKeys.ts`.

`useSetupReadiness` — composes `useModels`, `useMcpServers`, `usePolicies`, and `usePluginInstancesForAudience` to derive system readiness state (`hasModel`, `hasToolSource`, `hasAgent`, `nextStep`). The tools step (`hasToolSource`) is satisfied by an MCP server OR a tool-providing plugin instance (one whose `services` includes `tool`); the plugin read degrades to `false` on error and is intentionally excluded from `isError` so a 403 (e.g. the `approver` role) never breaks the checklist. Used by the dashboard and agents page empty states.

`useOptionsContext` — builds the `optionsContext` (`{search, degraded}`) passed to SchemaForm for plugin dynamic-option dropdowns; calls `/admin/plugins/{id}/instances/{iid}/options/{source}` via `apiFetch` (not TanStack Query) and tracks the `degraded` fallback flag. Shared by the plugin instance config tab and the audience editor (#622/#627).

### Storybook

48 story files covering components, dashboard widgets, form sections, and hook demonstrations. Stories use MSW for API mocking and have their own `.stories.module.css` for layout scaffolding where needed. Stories that need to demonstrate non-idle states (uploading, success, error) use Storybook `play` functions with `userEvent` to drive the component through real interactions rather than seeding cache state.

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
