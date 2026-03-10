# Gleipnir — Frontend Implementation Roadmap

**Stack:** React + TypeScript, Vite, CSS Modules, CodeMirror 6 (YAML editor), served via nginx, `/api` proxied to Go backend
**Real-time transport:** Server-Sent Events (SSE) for all server→client updates (see ADR-016). Mutations remain REST.
**Response envelope:** `{ data: T }` for success (HTTP 2xx), `{ error: string, detail?: string }` for failure (HTTP 4xx/5xx)
**Deployment:** Separate Docker container (`gleipnir-frontend`)
**Source of truth for data shapes:** `0001_initial_schema.sql`
**Design reference:** `docs/frontend_mockups/` — four JSX mockups defining the visual language and interaction patterns
**Phase scope:** This document covers EPIC-007 across v0.1 (foundation), v0.2 (approval UI), and v0.1-polish (design quality pass)

**Guiding principle:** Ship views first, extract shared patterns from working code, then polish. Every section in v0.1 should produce a usable screen. Shared components are extracted *from* views, not designed in isolation before views exist.

---

## 0. Frontend Architecture Decisions

These decisions affect every view and must be settled before implementation begins. They are more important than any visual detail in this document.

### State Management — TanStack Query + React Context

Use [TanStack Query](https://tanstack.com/query) (React Query) as the primary data layer. It handles fetching, caching, background refetching, and loading/error states — eliminating the need for hand-rolled fetch-on-mount patterns in every view.

- **Server state** (policies, runs, servers, tools) → TanStack Query. Each entity type gets a query key family (`['policies']`, `['runs', runId]`, `['servers', serverId, 'tools']`).
- **Client-only UI state** (which folder is expanded, slide-out open, filter selection) → React `useState` / `useReducer` local to the component that owns it. Lift only when two sibling components need the same state.
- **No global store** (no Redux, no Zustand) unless a clear need emerges. TanStack Query's cache *is* the global server state store.

### SSE-to-Cache Reconciliation

The hardest React problem in this app: when an SSE event arrives, how does it update the UI?

**Strategy: SSE events invalidate TanStack Query caches.**

1. A single `useSSE` hook connects to `GET /api/v1/events` and stays open for the app's lifetime (mounted in the root layout).
2. When an event arrives (e.g., `run.status_changed`), the hook calls `queryClient.invalidateQueries({ queryKey: ['runs', event.run_id] })` and any related queries (e.g., `['policies']` to update the dashboard list).
3. TanStack Query automatically refetches invalidated queries if they have active subscribers (i.e., a component is currently rendering that data). Queries with no subscribers just get marked stale.
4. For high-frequency events (`run.step_added` during a live run), use **optimistic cache updates** instead of refetching: `queryClient.setQueryData(['runs', runId, 'steps'], old => [...old, event.step])`. This avoids a network round-trip per step.

This keeps SSE handling in one place and lets every view stay reactive without subscribing to SSE events directly.

**Why not patch local component state?** Because multiple views may display the same data (stats bar, policy row, run detail). Cache invalidation updates all subscribers automatically.

### Data Fetching Patterns

All API calls go through a shared `apiFetch` wrapper:

```typescript
// Unwraps { data: T } envelope, throws typed ApiError on failure
async function apiFetch<T>(path: string, init?: RequestInit): Promise<T>
```

TanStack Query hooks wrap `apiFetch`:

```typescript
function usePolicies() {
  return useQuery({ queryKey: ['policies'], queryFn: () => apiFetch<Policy[]>('/policies') })
}

function useRun(id: string) {
  return useQuery({ queryKey: ['runs', id], queryFn: () => apiFetch<Run>(`/runs/${id}`) })
}
```

Mutations use `useMutation` with cache invalidation on success:

```typescript
function useSavePolicy() {
  return useMutation({
    mutationFn: (policy) => apiFetch('/policies', { method: 'POST', body: ... }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['policies'] }),
  })
}
```

This eliminates loading/error state boilerplate from every view.

### Error Boundaries

- **Root-level boundary:** catches unexpected render crashes. Shows "Something went wrong" with a retry button.
- **Per-view boundaries:** each route-level component is wrapped in its own error boundary. A crash in the MCP server view does not take down the dashboard.
- **Fetch errors are not boundary errors.** TanStack Query surfaces fetch failures through its `error` state, which views handle inline (error message at the point of failure, not a page-level crash).

---

## Design System

All views share a consistent design language defined in the mockups. Tokens and scale are defined upfront; shared *components* are extracted from views as they're built.

### CSS Architecture

**CSS Modules** — one `.module.css` file per component. Vite supports CSS Modules out of the box with zero config. No inline styles, no styled-components, no Tailwind.

Design tokens are defined as **CSS custom properties** on `:root` and consumed in module files. Dark theme is the default. Token structure supports a future light theme swap via `prefers-color-scheme` or a manual toggle — do not build the light theme in v0.1, but structure tokens so adding it later is a property swap, not a rewrite.

### Spacing Scale

All spacing uses a **4px base unit**. No arbitrary pixel values.

```
--space-1:   4px
--space-2:   8px
--space-3:  12px
--space-4:  16px
--space-6:  24px
--space-8:  32px
--space-12: 48px
--space-16: 64px
```

Every margin, padding, and gap must snap to this scale.

### Design Tokens

Defined as CSS custom properties on `:root`, scoped by `data-theme` attribute. The dark theme is the default. Alternative themes (light, color blindness) override only the properties they need to change. See section 12 for the full theme architecture, color blindness palettes, and the "never color alone" rule.

```css
:root {
  /* Background layers */
  --bg-canvas:     #0F1117;
  --bg-surface:    #131720;
  --bg-elevated:   #1E2330;
  --bg-topbar:     #0D1018;
  --bg-code:       #090C12;

  /* Borders */
  --border-subtle: #1E2330;
  --border-mid:    #253044;

  /* Text hierarchy */
  --text-faint:    #334155;
  --text-muted:    #475569;
  --text-second:   #94A3B8;
  --text-primary:  #E2E8F0;

  /* Semantic colors (dark theme defaults — overridden by color blindness schemes) */
  --color-blue:    #60A5FA;
  --color-orange:  #FB923C;
  --color-amber:   #F59E0B;
  --color-green:   #4ADE80;
  --color-red:     #F87171;
  --color-purple:  #A78BFA;
  --color-teal:    #34D399;

  /* Semantic aliases */
  --color-sensor:   var(--color-blue);
  --color-actuator: var(--color-orange);
  --color-feedback: var(--color-purple);
}
```

**Critical rule: never rely on color alone to convey meaning.** Every color signal must be paired with a shape, icon, or text label. See section 12 for the full audit checklist.

### Typographic Scale

Deliberate size steps — no 1px increments:

```css
:root {
  --text-xs:   11px;   /* Labels, metadata, timestamps */
  --text-sm:   13px;   /* Secondary content, table cells */
  --text-base: 15px;   /* Body text, primary content */
  --text-lg:   18px;   /* Section headers */
  --text-xl:   24px;   /* Page titles */
  --text-2xl:  32px;   /* Hero numbers (stats bar counters) */

  --font-body: 'IBM Plex Sans', system-ui, sans-serif;
  --font-mono: 'IBM Plex Mono', monospace;

  --weight-normal:   400;
  --weight-medium:   500;
  --weight-semibold: 600;
  --weight-bold:     700;  /* Reserved for the GLEIPNIR wordmark only */
}
```

Use `--weight-light` (300) for `--text-2xl` hero numbers in the stats bar.

### Motion System

All transitions use a consistent set of durations and curves:

```css
:root {
  --duration-fast:    120ms;  /* Hover states, color changes */
  --duration-normal:  200ms;  /* Panel slides, expand/collapse */
  --duration-slow:    350ms;  /* Page transitions, fade-outs */

  --ease-out:   cubic-bezier(0.16, 1, 0.3, 1);    /* Decelerate — for elements entering */
  --ease-in:    cubic-bezier(0.5, 0, 0.75, 0);     /* Accelerate — for elements leaving */
  --ease-spring: cubic-bezier(0.34, 1.56, 0.64, 1); /* Overshoot — for playful enter effects */
}
```

Specific animation patterns (stagger delays, scroll thresholds, blink intervals) are implementation details — discover them during development, not here. The tokens above are the vocabulary; views compose them as needed.

---

## Constraints and Ground Rules

- The UI reads and writes policy YAML directly — there is no separate form-based data model for policies. The form view parses YAML into fields for editing, then serializes back to YAML on save. The YAML string is the API payload.
- IDs are ULIDs (strings). Timestamps are ISO 8601 UTC strings.
- All `/api` requests are proxied by nginx to the Go backend — no direct backend URLs in the React app.
- No auth in v0.1. Basic auth (env-configured) is a v0.4 concern — do not build login flows now.
- Do not build anything marked v0.2 in the v0.1 milestone. v0.2 items are called out explicitly below.
- **Real-time updates via SSE (ADR-016).** See section 0 for the SSE-to-cache reconciliation strategy. On disconnect, show a non-blocking banner ("Connection lost — reconnecting…") that auto-dismisses on reconnect.
- **No inline styles.** All styling goes through CSS Modules consuming CSS custom properties. This is a hard rule.

---

## Route Structure

```
/                          → redirect to /dashboard
/dashboard                 → Dashboard (stats, policy list with latest run)
/policies/new              → New policy editor (dual-mode: form + YAML)
/policies/:id              → Edit policy editor (dual-mode: form + YAML)
/runs/:id                  → Run detail (reasoning timeline)
/mcp                       → MCP server management
```

---

## v0.1 — Foundation

### 1. Scaffolding and Infrastructure

**Goal:** Working React app that talks to the backend and is served by nginx. No views yet — just the shell.

- React app bootstrapped with Vite + TypeScript
- CSS Modules configured (Vite default, no extra setup)
- Global CSS file with `:root` custom properties (design tokens, spacing scale, typographic scale, motion system)
- TanStack Query provider at the root
- `apiFetch` wrapper (see section 0)
- `useSSE` hook mounted in root layout (see section 0 for reconciliation strategy):
  - Connects to `GET /api/v1/events`
  - Parses events by type and invalidates relevant TanStack Query caches
  - Auto-reconnects using `EventSource` native behavior
  - On disconnect: shows a connection-lost banner. On reconnect: dismisses it.
- nginx config:
  - Serve the React build from `/`
  - Proxy `/api/*` to the Go container (e.g. `http://gleipnir-api:8080`)
  - Set `X-Accel-Buffering: no` on SSE proxy responses (ADR-016)
- Dockerfile: multi-stage — Node build stage → nginx serve stage
- Global layout: **top bar navigation** with GLEIPNIR text wordmark and links to Dashboard, Policies, Servers
- Root-level and per-route error boundaries

**Acceptance criteria:**
- `docker-compose up` serves the React app on the configured port
- `/api` proxy routes correctly to the Go container
- Navigation renders and routes work
- Design tokens are available to all views via CSS custom properties
- SSE connection established on app load, reconnects on disconnect with visible banner
- No inline styles in any component

---

### 2. Dashboard

**Reference mockup:** `gleipnir-dashboard.jsx`

**Endpoints:**
- `GET /api/v1/policies` — list all policies
- `GET /api/v1/runs?policy_id=:id&limit=1` — latest run per policy (or included in policy response)

The dashboard is the main view. It shows a stats overview and a flat list of policies with their latest run status.

#### Loading State

On initial load, show **skeleton screens** in the shape of the final layout (stat cards + list rows). Do not show a spinner. Skeletons prevent layout shift and feel faster.

#### Stats Bar

Four stat cards at the top. Counter numbers use `--text-2xl` with `--weight-light` for a distinctive, airy feel:

| Stat | Source |
|---|---|
| Active runs | Count of runs with `running` status |
| Pending approvals | Count of pending approvals (amber highlight if > 0) |
| Policies | Total policy count |
| Tokens today | Sum of `token_cost` across latest runs |

Stats update in real time via SSE cache invalidation.

#### Pending Approvals Banner

**[v0.2 functionality — render as placeholder in v0.1]**

If any run has `waiting_for_approval` status, show a non-interactive banner: "N runs awaiting approval — approval UI available in v0.2."

In v0.2, this becomes the full approval card section (see section 6).

#### Policy List

A flat table of all policies. Each row shows:

- Policy name (linked to `/policies/:id`)
- TriggerChip (`webhook` / `cron` / `poll`)
- Latest run: StatusBadge + summary (truncated, single line)
- If running: spinner + "Executing…" instead of summary
- Relative timestamp of latest run ("3m ago")
- Duration of latest run
- Token cost of latest run

Clicking the run status/summary area navigates to `/runs/:id` for the latest run.

**"New Policy" button** in the top-right navigates to `/policies/new`.

**Empty state:**
When no policies exist:
- Centered content
- Headline: "No policies yet"
- Subtext: "Create your first policy to start running agents"
- Prominent "Create Policy" CTA button

**Live updates (SSE):**
- `run.status_changed` → TanStack Query invalidates `['policies']` and `['runs']`, affected rows re-render automatically
- `run.step_added` → invalidate run data if token cost changed

**Acceptance criteria:**
- Skeleton screens shown on initial load
- Policies listed with latest run status, timing, tokens
- "New Policy" button navigates to `/policies/new`
- Empty state renders with CTA when no policies exist
- SSE events update the list in real time

**Shared components extracted from this view:** StatusBadge, TriggerChip, SkeletonBlock. These emerge naturally — build them inline first, then extract when the pattern stabilizes.

---

### 3. Policy Editor (Dual-Mode: Form + YAML)

**Reference mockup:** `gleipnir-policy-editor.jsx`

**Used for both new policy creation and editing an existing policy.**

**Endpoints:**
- `GET /api/v1/policies/:id` — load existing policy YAML
- `POST /api/v1/policies` — create new policy
- `PUT /api/v1/policies/:id` — update existing policy
- `GET /api/v1/mcp/servers` + `GET /api/v1/mcp/servers/:id/tools` — for tool picker

The editor has two modes toggled by a Form/YAML switch in the top bar. Both modes edit the same underlying YAML string. Switching modes syncs the data.

#### Top Bar (Editor-specific)

- Breadcrumb: GLEIPNIR › Policies › `{policy-name}`
- Unsaved changes indicator (amber dot next to policy name when dirty)
- Form/YAML mode toggle (two-button group)
- Save button: active (blue) when dirty, muted when clean
- Delete policy button (with confirmation modal) — edit mode only
- `Cmd+S` / `Ctrl+S` saves the policy (prevent browser default save)

#### YAML Mode

- Full-height CodeMirror 6 editor (`@codemirror/lang-yaml`)
- Syntax validation indicator: "Valid YAML" (green) or "Invalid YAML" (red) with error message
- Malformed YAML blocks the save button
- On save error from API (validation failure): display error inline below the editor, do not navigate away

#### Form Mode

Single-column scrollable form.

**Form sections:**

1. **Policy Identity**
   - Name field (monospace input)
   - Description field
   - Folder field (text input, optional — for future dashboard grouping)

2. **Trigger**
   - Three trigger type cards (Webhook / Schedule / Poll) with icon, name, and description
   - Selected card has blue border + background tint
   - Webhook selected → show webhook endpoint URL (`POST /api/v1/webhooks/{policy-name}`) with copy button
   - Cron selected → show cron expression input
   - Poll selected → show interval, URL, method, headers, filter fields

3. **Capabilities (Tool Picker)**
   - Legend showing role colors: sensor (blue, "read-only, called freely") and actuator (orange, "world-affecting, optionally gated")
   - List of currently assigned tools, each showing:
     - RoleBadge + `server.tool_name` (monospace) + description
     - For actuators: approval toggle switch (amber when enabled)
     - Remove button (×)
   - "Add tool from registry" button → opens inline search panel:
     - Search input (filters by tool name, server name, or description)
     - Results list showing RoleBadge + `server.tool_name` + description
     - Clicking a result adds it to the capabilities list
     - Cancel button closes the search panel
   - Empty state: "No tools added yet. Add tools from the registry below."

4. **Task Instructions**
   - Multiline textarea for agent task prompt
   - Helper text: "The trigger payload (webhook body, poll result) is delivered as the agent's first message."

5. **Run Limits**
   - Two-column grid: max tokens per run (number input) + max tool calls per run (number input)

6. **Concurrency Behaviour**
   - Four option cards in a grid: Skip / Queue / Parallel / Replace
   - Each card has a label and short description
   - Selected card has blue border + background tint

**Syncing between modes:**
- Switching from Form → YAML: serialize form state to YAML string
- Switching from YAML → Form: parse YAML string into form fields. If YAML is malformed, stay in YAML mode and show an error.

**New policy flow:**
- Pre-populate with default YAML template
- Form mode starts with empty fields matching the template

**Acceptance criteria:**
- Dual-mode toggle works, data syncs between modes
- YAML round-trips correctly: save → reload produces the same YAML
- Form mode: all fields editable, tool picker works, approval toggles work
- Syntax error in YAML mode blocks save
- API validation error shown inline, does not navigate away
- Webhook URL is visible and copyable after save
- Dirty indicator shows when unsaved changes exist
- `Cmd+S` saves the policy

**Shared components extracted from this view:** RoleBadge (reused in MCP view and run timeline).

---

### 4. Run Detail — Reasoning Timeline

**Reference mockup:** `gleipnir-reasoning-timeline.jsx`

**Endpoints:**
- `GET /api/v1/runs/:id` — run metadata
- `GET /api/v1/runs/:id/steps` — all steps

**Layout:** Full-page view with a back button ("← Back to dashboard"), run metadata header, filter chips, and a chronological step timeline.

#### Loading State

Skeleton screens: a skeleton header block + several skeleton step cards matching the timeline layout.

#### Run Header

- Back button → navigate to `/dashboard`
- Policy name (linked to `/policies/:id`) + TriggerChip
- StatusBadge (large)
- Metadata grid: Run ID (truncated ULID), Started (absolute time), Duration (or "in progress" with elapsed timer), Tokens (total), Tool calls (count)
- If status is `failed` or `interrupted`: error message displayed in a red-bordered box
- Trigger payload: collapsible JSON block

#### Filter Chips

Row of filter buttons: All | Thoughts | Calls | Results | Errors

Each chip shows a count badge. Clicking a chip filters the timeline. `capability_snapshot` steps are excluded from filter counts — they're infrastructure, not agent reasoning (ADR-018).

#### Step Timeline

Vertical timeline with a connector line between steps. Each step has an icon on the left and a content card on the right.

**Step type rendering:**

| Type | Icon | Content |
|---|---|---|
| `capability_snapshot` | Shield icon | Collapsed by default. Summary: "capability snapshot — N tools". Expand to show tool list with RoleBadge and approval status. Renders at the top of the timeline (step 0). |
| `thought` | Gray dot | Text block, agent reasoning. Token cost shown. |
| `tool_call` | Blue `→` (sensor) or Orange `→` (actuator) | Tool name (monospace, colored by role) + RoleBadge. Input JSON in a collapsible block with copy button. |
| `tool_result` | Green `←` (success) or Red `←` (error) | Output in a `<pre>` block with copy button. If `is_error: true`, red border and error styling. |
| `approval_request` | Amber shield | [v0.1 placeholder] "Approval requested — approval UI available in v0.2." [v0.2] Full inline approval card (see section 7). |
| `feedback_request` | Purple speech bubble | [v0.1 placeholder] "Feedback requested — feedback UI available in v0.2." |
| `feedback_response` | Purple speech bubble (filled) | [v0.1 placeholder] "Feedback response received." |
| `error` | Red `!` | Error message in a red-bordered box. |
| `complete` | Green checkmark | Summary text in a green-bordered success card. Token cost shown. |

#### Pagination for Long Runs

For runs with many steps, load the first 50 steps. Show a "Load more" button at the top of the timeline to fetch earlier steps. This avoids the complexity of virtual scrolling with variable-height items.

**Live updates (SSE):**
- `run.step_added` → optimistic cache update appends the step. Auto-scroll to bottom if user is near the bottom. If user has scrolled up, show a "New steps ↓" pill that scrolls down on click.
- `run.status_changed` → invalidate run query, header updates automatically.

**Acceptance criteria:**
- Skeleton screens on initial load
- All step types render correctly
- Live runs append new steps via SSE
- JSON blobs in tool_call/tool_result are collapsible with copy buttons
- Token cost per step is visible
- Filter chips work and show correct counts
- Capability snapshot renders at the top, collapsed by default
- Back button returns to dashboard
- "New steps ↓" pill appears when user has scrolled away from bottom during live run
- "Load more" button works for runs with 50+ steps

**Shared components extracted from this view:** CollapsibleJSON, CopyBlock (these emerge from repeated use of collapsible JSON + copy patterns across tool_call, tool_result, and trigger payload).

---

### 5. MCP Server Management

**Reference mockup:** `gleipnir-mcp-registry.jsx`

**Endpoints:**
- `GET /api/v1/mcp/servers` — list registered servers
- `POST /api/v1/mcp/servers` — register new server
- `DELETE /api/v1/mcp/servers/:id` — remove server
- `POST /api/v1/mcp/servers/:id/discover` — trigger re-discovery
- `GET /api/v1/mcp/servers/:id/tools` — list tools with capability roles
- `PATCH /api/v1/mcp/tools/:id` — update capability role

**Layout:** Full-page view with global tool stats, an "Add Server" button, and expandable server cards.

#### Loading State

Skeleton screens: stat counters as skeleton blocks + 2-3 skeleton server cards.

#### Global Stats

- Total tools count, sensors count, actuators count, unassigned count
- "Add Server" button → opens modal

#### Add Server Modal

- Fields: Name (text input) + URL (text input, monospace, placeholder: `http://my-server:8080`)
- Cancel / Add Server buttons
- On success: server card appears in the list, auto-triggers discovery

#### Server Cards

One card per registered MCP server:

**Card header:**
- Health indicator: text label ("reachable" / "unreachable" / "checking") with colored dot
- Server name
- Server URL (monospace, muted)
- Last discovered timestamp (relative: "3h ago") or "Never discovered"
- "Discover" button (triggers re-discovery, shows spinner while running)
- Expand/collapse chevron for tool list
- Delete button (trash icon) — with confirmation modal ("This will remove N tools from the registry.")

**Unassigned role warning:**
If any tools on the server have no capability role assigned, show an amber banner: "N tools need a capability role assigned before they can be used in policies"

**Tool list (per server, expandable):**

Each tool row:
- Tool name (`server.tool_name`, monospace)
- Description
- Input schema: expandable JSON block
- Capability role: **inline dropdown** to change role (`sensor` | `actuator` | `feedback`). Saves on change via `PATCH /api/v1/mcp/tools/:id`.

Re-discovery simply refreshes the tool list in place. The user sees the updated list and can check what changed. A structured diff view is a v0.1-polish enhancement (see section 10).

**Acceptance criteria:**
- Skeleton screens on initial load
- Servers listed with health indicator (text + dot) and last discovery time
- Add server modal works, auto-discovers on creation
- Tool capability roles editable via inline dropdown
- Unassigned role warning banner when applicable
- Delete server works with confirmation
- Re-discovery refreshes the tool list

---

## v0.2 — Approval UI

> Build this milestone after the Go approval gate backend (EPIC-006) is complete and the approval endpoints are available. Do not stub approval UI into v0.1 beyond the placeholder cards noted above.

### 6. Dashboard — Pending Approvals Section

**Reference mockup:** `gleipnir-dashboard.jsx` (ApprovalsSection, ApprovalCard)

**Endpoint:** `GET /api/v1/approvals?status=pending`

Replaces the v0.1 placeholder banner with full interactive approval cards.

**Section header:**
- Amber indicator + "Pending Approvals" + count badge

**Approval card (one per pending approval):**

- **Header row:**
  - Amber alert icon
  - Policy name + TriggerChip
  - Agent summary (1-2 sentence description of what the agent wants to do)
  - Countdown timer showing time remaining before timeout (`expires_at`). Two states:
    - Normal: amber text
    - Expiring soon (< 5 minutes): timer turns red, border changes to amber
  - "Show reasoning" / "Hide reasoning" toggle

- **Proposed action block** (always visible):
  - "PROPOSED ACTION" label
  - Tool name in an orange monospace badge + "actuator · approval required" label
  - Proposed input as formatted, collapsible JSON

- **Agent reasoning trace** (collapsible, hidden by default):
  - "AGENT REASONING" label
  - Mini timeline showing the steps that led to this approval request (thoughts, tool calls, results)
  - Same visual style as the full reasoning timeline but compact

- **Action row:**
  - "Run paused, waiting for your decision" (muted)
  - Reject button (red outline, ✕ icon)
  - Approve button (green outline, ✓ icon)

- **Confirm flow** (after clicking Approve or Reject):
  - Replaces the action row with: optional note textarea + Cancel button + "Confirm Approve" / "Confirm Reject" button

**After decision:**
- Card transitions to a compact receipt row showing the decision
- Receipt shows status icon + "Approved"/"Rejected" + policy name + tool name
- "confirming…" indicator until SSE confirms the status change, then receipt fades away

**Live updates (SSE):**
- `approval.created` → new approval card appears
- `approval.resolved` → card transitions to receipt, then collapses

**Acceptance criteria:**
- All pending approvals visible with countdown timers
- Timer visual change at < 5min threshold
- Agent reasoning trace expandable per approval
- Approve/reject actions require confirmation with optional note
- Receipts show after decision, disappear after SSE confirmation
- SSE events update approval state in real time

### 7. Run Detail — Approval Step Cards

Upgrade the placeholder `approval_request` step cards from v0.1:

**Reference mockup:** `gleipnir-reasoning-timeline.jsx` (approval_request step type)

- Show tool name (orange monospace badge) + RoleBadge ("actuator")
- Proposed input as formatted JSON with copy button
- If status is `pending`: show Approve / Reject buttons inline, with the same confirm flow as the dashboard
- Countdown timer if status is `pending` (same two-state behavior as dashboard)
- If status is `approved` / `rejected` / `timeout`: show decided state with icon, timestamp, and note if present

### 8. Feedback Round-Trip

Upgrade `feedback_request` and `feedback_response` step cards:

- `feedback_request`: show the agent's message in a purple-bordered card. If awaiting response, show a text input + "Send Response" button
- `feedback_response`: show the operator's response in a visually distinct style (different background color)

---

## v0.1-polish — Design Quality Pass

> These items elevate the UI from functional to polished. They can be worked on after v0.1 features ship. They are not blockers for v0.1 launch but should ship before the UI is shown externally.
>
> **Key principle:** These are enhancements to working views, not speculative design-in-a-vacuum. Each item here builds on patterns that already exist in the v0.1 codebase.

### 9. Dashboard — Folder Grouping

Upgrade the flat policy list to a folder→policy hierarchy:

- Policies grouped by their `folder` field (optional string in the policy YAML, defaults to "Ungrouped")
- **Folder row (collapsed by default):** chevron toggle + folder name + policy count + status indicator (derived from worst-case status across the folder's policies)
- Expand/collapse animates with smooth height transition
- Expanded folder shows the same policy rows as the v0.1 flat list, now nested under their folder
- Per-policy run history: expandable list of previous runs (paginated) with a history toggle button

This is the right time for folder grouping because v0.1 established the policy list patterns and you now know what data is available.

### 10. MCP Discovery Diff View

Upgrade re-discovery from "refresh in place" to a structured diff:

- After re-discovery, if tools changed, show a diff section on the server card:
  - **Added tools** — green left-border, `+` icon, tool name + description + role dropdown
  - **Removed tools** — red left-border, `−` icon, tool name + description, strikethrough
  - **Modified tools** — amber left-border, `~` icon, tool name + old→new description
- Each section has an "Accept" action
- Show affected policies that reference changed/removed tools

### 11. Keyboard Navigation and Command Palette

Add keyboard shortcuts based on actual usage patterns from v0.1:

- **Command palette** (`Cmd+K` / `Ctrl+K`): centered modal with search input. Searches across policies, runs, and actions. Use [cmdk](https://cmdk.paco.me/) as the base component.
- **Global:** `?` opens shortcut overlay, `Esc` goes back / closes panels
- **Dashboard:** `j/k` to navigate rows, `Enter` to expand/open, `n` for new policy
- **Timeline:** `j/k` to navigate steps, `Enter` to expand/collapse, `c` to copy step content
- **Editor:** `Cmd+S` to save (this one ships in v0.1 since it's essential)

Add a `?` shortcut overlay listing all available shortcuts grouped by context. Show a subtle `? shortcuts` hint in the corner of each page.

### 12. Theme and Accessibility Foundation

#### Theme Architecture

All visual theming runs through CSS custom properties on `:root`, selected by a `data-theme` attribute on `<html>`. This single mechanism supports dark/light mode AND color blindness schemes.

- Create `themes/dark.css` that sets all `:root` variables (this is the current default)
- Create empty `themes/light.css` placeholder
- Create `themes/cb-deuteranopia.css` and `themes/cb-tritanopia.css` (see below)
- Theme selector: dropdown in top bar offering Dark (default) | Deuteranopia | Tritanopia
- Store selection in `localStorage`, apply before first paint (inline `<script>` in `index.html`)
- Color blindness themes compose with dark/light — e.g., `data-theme="dark cb-deuteranopia"`

#### Color Blindness Schemes

The default palette uses red/green as opposing signals (failed/success). This is indistinguishable for ~8% of males with deuteranopia. The fix is two-part: alternative palettes AND the structural "never color alone" rule.

**Deuteranopia / Protanopia scheme** (red-green deficiency, most common):

```css
[data-theme~="cb-deuteranopia"] {
  --color-green:   #56B4E9;  /* Sky blue — replaces green */
  --color-red:     #D55E00;  /* Vermillion — replaces red */
  --color-amber:   #E69F00;  /* Orange-yellow — unchanged */
  --color-blue:    #0072B2;  /* Darker blue — sensors */
  --color-orange:  #CC79A7;  /* Rose pink — actuators */
  --color-purple:  #9467BD;  /* Kept similar — feedback */
  --color-teal:    #009E73;  /* Bluish green — poll triggers */
}
```

**Tritanopia scheme** (blue-yellow deficiency):

```css
[data-theme~="cb-tritanopia"] {
  --color-green:   #009E73;
  --color-red:     #D55E00;
  --color-amber:   #CC79A7;
  --color-blue:    #56B4E9;
  --color-orange:  #E69F00;
  --color-purple:  #882255;
  --color-teal:    #117733;
}
```

These values are derived from the [Bang Wong color palette](https://www.nature.com/articles/nmeth.1618), designed for universal accessibility in scientific visualization.

#### "Never Color Alone" Rule

**Every piece of information conveyed by color must also be conveyed by shape, icon, or text.** Audit every color-dependent element:

| Element | Color signal | Non-color redundancy required |
|---|---|---|
| StatusBadge | Green/blue/amber/red dot | ✅ Has text label ("Complete", "Failed", etc.) |
| TriggerChip | Blue/purple/teal text | ✅ Has text label ("webhook", "cron", "poll") |
| RoleChip | Blue/orange/purple text | ✅ Has text label ("sensor", "actuator", "feedback") |
| Folder status dot | Color-only | ❌ **Add tooltip showing status text** |
| Health indicator (MCP) | Green/red/amber dot | ✅ Has text label (fixed in v0.1 — "reachable"/"unreachable"/"checking") |
| Discovery diff borders | Green/red/amber border | ❌ **Add prefix icon: `+`, `−`, `~`** |
| Approve/Reject buttons | Green/red | ❌ **Add ✓ and ✕ icons** |
| Tool call icon color | Blue vs orange | ❌ **Show RoleBadge next to tool name** |

Items marked ❌ must be fixed when they ship. Non-color redundancy benefits everyone, not just color-blind users.

#### ARIA and Screen Reader Basics

Not a full WCAG audit, but establish the foundation:

- Icon-only buttons (`×`, chevron, history toggle) need `aria-label` attributes
- SSE-driven status changes update an `aria-live="polite"` region
- Approval countdown timers need `aria-label` with time in words
- The command palette (v0.1-polish) follows the WAI-ARIA combobox pattern
- Focus management: modals trap focus; closing returns focus to the trigger element

#### Reduced Motion

Respect `prefers-reduced-motion`:

```css
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after {
    animation-duration: 0.01ms !important;
    animation-iteration-count: 1 !important;
    transition-duration: 0.01ms !important;
  }
}
```

Content still appears — it just appears instantly.

### 13. Animation Polish

Audit all transitions for consistency with the motion system tokens. Key patterns to standardize:

- **Expand/collapse** (folders, tool lists, JSON blocks): smooth height animation
- **Step card entry** (timeline): subtle enter animation
- **Status indicator transitions**: no hard color cuts when SSE updates change state
- **Modal enter/exit**: fade backdrop + slide content
- **Hover states**: all interactive elements respond on `--duration-fast`

### 14. Dashboard — Run Detail Slide-Out

Add a slide-out panel to the dashboard for quick run inspection without navigating away:

- Clicking a policy's run status opens a side panel with: StatusBadge, run summary, metadata (ID, start time, duration, tokens, tool calls)
- "View reasoning timeline" button → navigates to `/runs/:id`
- Close button or `Esc` dismisses the panel

This ships in polish because the v0.1 dashboard is fully functional without it — clicking a run navigates directly to `/runs/:id`.

---

## API Contract Assumptions

The following endpoints are assumed. Backend must confirm or adjust before frontend work begins on each section.

```
GET    /api/v1/policies
POST   /api/v1/policies
GET    /api/v1/policies/:id
PUT    /api/v1/policies/:id
DELETE /api/v1/policies/:id

GET    /api/v1/runs
GET    /api/v1/runs/:id
GET    /api/v1/runs/:id/steps

GET    /api/v1/mcp/servers
POST   /api/v1/mcp/servers
DELETE /api/v1/mcp/servers/:id
POST   /api/v1/mcp/servers/:id/discover
GET    /api/v1/mcp/servers/:id/tools
PATCH  /api/v1/mcp/tools/:id              — update capability_role

GET    /api/v1/approvals                   — [v0.2]
POST   /api/v1/approvals/:id/approve       — [v0.2]
POST   /api/v1/approvals/:id/reject        — [v0.2]

GET    /api/v1/events                      — SSE stream (text/event-stream)

GET    /api/v1/health
```

**Response envelope (decided):** `{ data: T }` for success (HTTP 2xx), `{ error: string, detail?: string }` for failure (HTTP 4xx/5xx).

**SSE event format:**
```
event: run.status_changed
id: <monotonic-event-id>
data: {"run_id": "...", "status": "running", "started_at": "...", "token_cost": 0}

event: run.step_added
id: <monotonic-event-id>
data: {"run_id": "...", "step": { ...step object... }}

event: approval.created
id: <monotonic-event-id>
data: {"approval_id": "...", "run_id": "...", "tool_name": "...", "reasoning_summary": "..."}

event: approval.resolved
id: <monotonic-event-id>
data: {"approval_id": "...", "resolution": "approved|rejected|timeout"}
```

**Discovery response shape** (from `POST /api/v1/mcp/servers/:id/discover`):
```json
{
  "data": {
    "added": [
      { "name": "kubectl.rollout_restart", "description": "...", "suggested_role": "actuator" }
    ],
    "removed": [
      { "name": "kubectl.get_logs", "description": "..." }
    ],
    "modified": [
      { "name": "kubectl.scale", "description": "new desc", "previous_description": "old desc" }
    ],
    "unchanged_count": 3
  }
}
```

---

## Deferred (Do Not Build)

The following are explicitly out of scope for v0.1, v0.1-polish, and v0.2:

- Login / auth UI (v0.4, basic auth is env-configured at the nginx level)
- Slack configuration UI (v0.5)
- Cron miss alerts UI (v0.4)
- Automatic MCP drift detection (v0.4) — v0.1 has manual re-discovery
- Multi-user / user identity UI (post v0.4)
- Policy dry-run mode
- Any agent-to-agent or multi-agent UI
- Full light theme implementation (v0.1-polish sets up the token foundation; light theme colors are a future task)
- Full WCAG 2.1 AA audit (v0.1-polish covers the structural foundation; a formal audit is deferred)
- Sound/audio notifications (consider for v0.3 with approval urgency)
- Streaming text effect for thoughts (if the backend adds partial step delivery via SSE, revisit then — do not simulate streaming from fully-received data)
- Storybook (add when there are 10+ shared components worth cataloguing, not before)
- Logo/branding design (track separately as a creative brief, not an engineering task)
- Policy cross-references on MCP tool rows (useful but not v0.1 — requires a join query the backend may not support yet)
