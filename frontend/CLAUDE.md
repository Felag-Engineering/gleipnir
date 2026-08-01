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

Routes are defined in `src/App.tsx`; all authenticated routes render inside a shared `Layout` (sidebar + content area) and each route has its own `errorElement` boundary. Legacy redirects: `/mcp` → `/tools`, `/users` → `/admin/users`. Gotcha: the plugin instance editor's Config tab renders the instance-level `config_schema` from `usePluginInstanceDetail`, NOT the listing endpoint's per-channel schema (#602).

## Design system

Design tokens live in `src/tokens.css`. The system supports dark, light, and system-preference themes via `data-theme` attribute on `:root`.

### Color

Nordic Forest palette — values in `src/tokens.css`. Gotcha: `--color-blue` holds warm orange (see the tokens.css comment). Capability aliases: `--color-tool: var(--color-blue)`, `--color-feedback: var(--color-purple)`.

### Typography

IBM Plex Sans / IBM Plex Mono via `--font-body`/`--font-mono`; size and weight scales in tokens.css. `--text-2xs: 10px` is micro type for badges/chips only — prefer `--text-xs` (11px) for normal small text; never hardcode `font-size: 10px`.

### Spacing

4px base grid via CSS custom properties (`--space-1` through `--space-16`, plus `--space-20` and `--space-32` for larger fixed sizes). All margins, padding, and gaps snap to multiples of 4px.

### Radii

Small-element `border-radius` comes from the radius scale in tokens.css — never hardcode `2/3/4/6px`. Larger structural radii (modals/pills at 10/12/14/16/999px) are intentionally raw values for now — distinct one-offs, not part of the small-element drift the scale addresses.

### Motion

Duration/easing tokens live in tokens.css; the reduced-motion media query sets all durations to 0ms.

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

### Data fetching

All API calls go through a shared `apiFetch<T>(path, init?)` wrapper (`src/api/fetch.ts`) that unwraps `{ data: T }` envelopes and throws typed `ApiError` on failure. TanStack Query hooks wrap `apiFetch`.

### SSE integration

A single `useSSE` hook at the root layout connects to `GET /api/v1/events`. On event arrival, it invalidates relevant TanStack Query caches. For high-frequency `run.step_added` events, use optimistic cache updates instead of refetching.

Event types: `run.status_changed`, `run.step_added`, `approval.created`, `approval.resolved`.

The Go SSE handler sets `X-Accel-Buffering: no` for compatibility with upstream reverse proxies (ADR-016).

## Formatting utilities

Canonical formatting helpers live in `src/utils/format.ts`.

`src/utils/inlineMarkdown.ts` — `renderInlineMarkdown(text)` tokenizes bold (`**`), italic (`*`/`_`), and inline code (`` ` ``) and returns a `ReactNode[]` for use inside JSX. No external markdown dependency.

## API types

All API response types are defined in `src/api/types.ts`, with comments mapping each type to its Go backend counterpart (e.g., `ApiRun` matches `trigger/runs_handler.go → RunSummary`).

## API surface

Endpoint reference lives in the root `CLAUDE.md` ("Key API surface") and `src/api/types.ts`. Response envelope: `{ data: T }` for success, `{ error: string, detail?: string }` for failure.

## Component structure

### Pages (`src/pages/`)

10 page components, each with a corresponding `.module.css`. Pages are thin wrappers that compose hooks and components.

### Components (`src/components/`)

Organized by feature area (Layout, dashboard/, AgentEditor/, AgentList/, RunDetail/, MCPPage/, admin/, form/, plus shared primitives) — see the directory for the inventory. Conventions that aren't visible from a quick read:

- **MCP server header editor** (`ServerDetailModal`): existing header names are read-only, value fields start empty with a placeholder; saves fan out via `useSetMcpServerHeader`/`useDeleteMcpServerHeader`; no redaction sentinel (ADR-039).
- **SchemaForm** renders plugin config forms (RJSF was removed in #627 — do not reintroduce `@rjsf/*`). `optionsContext` wires `AsyncCombobox` for `x-gleipnir-options` properties (degrades to free text when the plugin can't serve options); required markers render via CSS `::after` to keep DOM text clean for `getByLabelText`. The audience editor's `response_buttons` field uses `AudienceEditor/ResponseButtonsEditor` and omits the key when empty so the backend Approve/Reject default applies.
- **Toast** (`ToastProvider`/`useToast`): fire a success toast only when the UI has no other confirmation — actions that change a visible badge/toggle in place (pause/resume, tool enable/disable, user activate/deactivate) get NO success toast, only an error toast; `Agent saved`/`Agent deleted`/`User created` keep theirs. Auto-dismiss 4s/6s, `duration:0` sticky, stack capped at 3, identical toasts deduped.

### Hooks (`src/hooks/`)

~29 custom hooks: `use{Resource}` wraps `useQuery`, `use{Action}` wraps `useMutation`; all keys via `queryKeys.ts`. Gotcha: `useSetupReadiness` deliberately excludes the plugin-instances read from `isError` (degrades to `false`) so a 403 (e.g. the `approver` role) never breaks the setup checklist.

### Storybook

Stories use MSW for API mocking. Stories demonstrating non-idle states (uploading, success, error) use `play` functions with `userEvent` to drive real interactions rather than seeding cache state.

## Testing

- **Unit tests:** Vitest with jsdom, `npm run test:unit`. Tests live alongside components (`*.test.tsx`).
- **Component stories:** Storybook with `@storybook/addon-vitest` for story-level assertions.
- **API mocking:** MSW (Mock Service Worker) for both tests and Storybook stories.
- **Browser tests:** Playwright via `@vitest/browser-playwright` (not yet widely used).
