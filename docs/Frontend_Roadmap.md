# Gleipnir — Frontend Implementation Roadmap

**Stack:** React + TypeScript, Vite, CSS Modules, CodeMirror 6 (YAML editor), served via nginx, `/api` proxied to Go backend
**Real-time transport:** Server-Sent Events (SSE) for all server→client updates (see ADR-016). Mutations remain REST.
**Response envelope:** `{ data: T }` for success (HTTP 2xx), `{ error: string, detail?: string }` for failure (HTTP 4xx/5xx)
**Deployment:** Separate Docker container (`gleipnir-frontend`)
**Source of truth for data shapes:** `0001_initial_schema.sql`
**Design reference:** `docs/frontend_mockups/` — four JSX mockups defining the visual language and interaction patterns
**Phase scope:** This document covers EPIC-007 across v0.1 (foundation), v0.2 (approval UI), and v0.1-polish (design quality pass)

---

## Design System

All views share a consistent design language defined in the mockups. Extract these as a shared theme before building any views.

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

Every margin, padding, and gap must snap to this scale. This creates visual rhythm.

### Design Tokens

Defined as CSS custom properties on `:root`, scoped by `data-theme` attribute. The dark theme is the default. Alternative themes (light, color blindness) override only the properties they need to change. See section 14 for the full theme architecture, color blindness palettes, and the "never color alone" rule.

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

**Critical rule: never rely on color alone to convey meaning.** Every color signal must be paired with a shape, icon, or text label. See section 14 for the full audit checklist.

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

Use `--weight-light` (300) for `--text-2xl` hero numbers in the stats bar to create a distinctive, airy feel that contrasts with the dense data below.

### Motion System

All transitions use a consistent set of durations and curves:

```css
:root {
  --duration-fast:    120ms;  /* Hover states, color changes */
  --duration-normal:  200ms;  /* Panel slides, expand/collapse */
  --duration-slow:    350ms;  /* Page transitions, fade-outs */

  --ease-out:   cubic-bezier(0.16, 1, 0.3, 1);    /* Decelerate — for elements entering */
  --ease-in:    cubic-bezier(0.5, 0, 0.75, 0);     /* Accelerate — for elements leaving */
  --ease-spring: cubic-bezier(0.34, 1.56, 0.64, 1); /* Overshoot — for step card slide-in */
}
```

List items (folder rows, tool rows, step cards) use a **stagger pattern**: each item delays by 30ms × index for initial render. Maximum 10 items staggered, remainder appear instantly.

### Shared Components

Extract the following as reusable components with CSS Modules. Each component should have all its visual states defined (default, hover, active, disabled, loading). Build these in **Storybook** before integrating into views.

- **StatusBadge** — colored dot + label for run status (`complete`, `running`, `waiting_for_approval`, `failed`, `interrupted`). Pulsing animation for active states.
- **TriggerChip** — colored monospace label (`webhook` blue, `cron` purple, `poll` teal)
- **RoleChip / RoleBadge** — capability role indicator (`sensor` blue, `actuator` orange, `feedback` purple)
- **Spinner** — rotating SVG arc, configurable size and color
- **CopyBlock** — wrapper that shows a "copy" button on hover, copies text to clipboard, shows "✓ copied" for 1.8s
- **SkeletonBlock** — pulsing gray rectangle placeholder for loading states. Configurable width, height, and border-radius. Used in place of spinners for initial data loads.
- **CollapsibleJSON** — formatted JSON with syntax coloring, expand/collapse toggle, and CopyBlock wrapper
- **ConnectionBanner** — persistent top-of-page banner for SSE disconnect state: "Connection lost — reconnecting…" with retry indicator. Dismisses automatically when SSE reconnects.

---

## Constraints and Ground Rules

- The UI reads and writes policy YAML directly — there is no separate form-based data model for policies. The form view parses YAML into fields for editing, then serializes back to YAML on save. The YAML string is the API payload.
- IDs are ULIDs (strings). Timestamps are ISO 8601 UTC strings.
- All `/api` requests are proxied by nginx to the Go backend — no direct backend URLs in the React app.
- No auth in v0.1. Basic auth (env-configured) is a v0.4 concern — do not build login flows now.
- Do not build anything marked v0.2 in the v0.1 milestone. v0.2 items are called out explicitly below.
- **Real-time updates via SSE (ADR-016).** The frontend opens an `EventSource` connection to receive `run.status_changed`, `run.step_added`, `approval.created`, and `approval.resolved` events. Mutations (approve, reject, CRUD) are normal REST calls. Polling is the fallback only if SSE connection drops and `EventSource` auto-reconnect hasn't recovered. On disconnect, show the `ConnectionBanner` component.
- **No inline styles.** All styling goes through CSS Modules consuming CSS custom properties. This is a hard rule — PRs with inline `style={}` will be rejected.

---

## Route Structure

```
/                          → redirect to /dashboard
/dashboard                 → Dashboard (stats, approvals, folder→policy→run hierarchy)
/policies/new              → New policy editor (dual-mode: form + YAML)
/policies/:id              → Edit policy editor (dual-mode: form + YAML)
/runs/:id                  → Run detail (reasoning timeline)
/mcp                       → MCP server management
/approvals                 → [v0.2] Dedicated approval queue (dashboard handles approvals in v0.1)
```

---

## v0.1 — Foundation

### 1. Scaffolding and Infrastructure

**Goal:** Working React app that talks to the backend and is served by nginx.

- React app bootstrapped with Vite + TypeScript
- **CSS Modules** configured (Vite default, no extra setup)
- **Storybook** configured for component development — all shared components built and viewable in isolation before integration
- Global CSS file with `:root` custom properties (design tokens, spacing scale, typographic scale, motion system)
- nginx config:
  - Serve the React build from `/`
  - Proxy `/api/*` to the Go container (e.g. `http://gleipnir-api:8080`)
  - Set `X-Accel-Buffering: no` on SSE proxy responses (ADR-016)
- Dockerfile: multi-stage — Node build stage → nginx serve stage
- Global layout: **top bar navigation** with GLEIPNIR wordmark and links to Dashboard, Policies, Servers. Approval count badge in top-right when pending approvals exist (pulsing amber dot + count).
- Basic error boundary at the root level with a styled fallback ("Something went wrong" with a retry button, not a white screen)
- A shared `apiFetch` wrapper that:
  - Prefixes all requests with `/api/v1`
  - Parses JSON responses, unwrapping `{ data: T }` envelope on success
  - On error (HTTP 4xx/5xx), parses `{ error: string, detail?: string }` and throws a typed error
- A shared `useSSE` hook (or `EventSource` wrapper) that:
  - Connects to `GET /api/v1/events` (SSE endpoint)
  - Parses `text/event-stream` events by type (`run.status_changed`, `run.step_added`, `approval.created`, `approval.resolved`)
  - Auto-reconnects using `EventSource` native behavior with `Last-Event-ID` support
  - Components subscribe to specific event types and optionally filter by resource ID
  - On disconnect: triggers `ConnectionBanner` display. On reconnect: dismisses banner.
- **Command palette** (`Cmd+K` / `Ctrl+K`):
  - Opens a centered modal overlay with a search input
  - Searches across policies (by name), runs (by ID), and actions ("new policy", "approve", "servers")
  - Results grouped by type with keyboard navigation (↑/↓ to select, Enter to go, Esc to close)
  - Use [cmdk](https://cmdk.paco.me/) (~4KB) as the base component
  - Initially populated with static actions + policy/run names from cached API data
- Shared components built in Storybook: StatusBadge, TriggerChip, RoleBadge, Spinner, CopyBlock, SkeletonBlock, CollapsibleJSON, ConnectionBanner

**Acceptance criteria:**
- `docker-compose up` serves the React app on the configured port
- `/api` proxy routes correctly to the Go container
- Navigation renders and routes work
- Design tokens and shared components are available to all views
- Storybook is accessible in dev mode with all shared components
- Command palette opens with `Cmd+K`, searches policies and actions
- ConnectionBanner appears on SSE disconnect, auto-dismisses on reconnect
- No inline styles in any component

---

### 2. Dashboard

**Reference mockup:** `gleipnir-dashboard.jsx`

**Endpoints:**
- `GET /api/v1/policies` — list all policies (response includes `folder` field from YAML)
- `GET /api/v1/runs?policy_id=:id&limit=1` — latest run per policy (or included in policy response)
- `GET /api/v1/runs?policy_id=:id&offset=1` — run history for expanded policy rows

The dashboard is the main view. It replaces the separate "Policy List" and "Run List" views from the original roadmap with a unified folder→policy→run hierarchy.

#### Loading State

On initial load, show **skeleton screens** in the shape of the final layout:
- Stats bar: four skeleton rectangles matching card dimensions
- Folder list: 3 skeleton rows with gray pulsing blocks for folder name, policy count, and token total

Do not show a spinner. Skeletons feel faster and prevent layout shift.

#### Stats Bar

Four stat cards at the top. Counter numbers use `--text-2xl` (32px) with `--weight-light` (300) for a distinctive, airy feel:

| Stat | Source |
|---|---|
| Active runs | Count of runs with `running` status |
| Pending approvals | Count of pending approvals (pulsing amber if > 0) |
| Folders | Count of distinct folders |
| Tokens today | Sum of `token_cost` across latest runs |

Stats update in real time via SSE `run.status_changed` and `approval.*` events.

#### Pending Approvals Section

**[v0.2 functionality — render as placeholder in v0.1]**

In v0.1, if any run has `waiting_for_approval` status, show a non-interactive banner: "N runs awaiting approval — approval UI available in v0.2."

In v0.2, this section renders full approval cards (see section 6).

#### Folder → Policy → Run Hierarchy

Policies are grouped by their `folder` field (an optional string in the policy YAML, defaults to "Ungrouped").

**Folder row (collapsed by default):**
- Chevron toggle + folder icon + folder name
- Status dot — derived from the worst-case status across the folder's policies: amber if any approval pending > red if any failed > blue if any running > green if all complete. Status dot color transitions smoothly (`transition: background var(--duration-normal) ease`) when SSE updates change the state.
- Policy count chip ("3 policies")
- "approval pending" chip if any policy in the folder has a `waiting_for_approval` run
- Total token cost (sum of latest runs)

**Folder row (expanded) — shows a column header and policy rows:**

Column headers: `Policy / Latest run | Status | When | Duration | Tokens | History`

Expand/collapse animates with smooth height transition (`max-height` + `overflow: hidden`, duration `--duration-normal`).

**Policy row** (one per policy in the folder):
- Policy name + trigger chip + latest run summary (truncated, single line)
- If running: show spinner + "Executing…" instead of summary
- StatusBadge of the latest run
- Relative timestamp of latest run ("3m ago")
- Duration of latest run
- Token cost of latest run
- History toggle button (clock icon) — expands run history

**Run history (expanded via history toggle):**
- Table of previous runs for that policy (paginated, 4 per page)
- Columns: Status | Summary | When | Duration | Tokens
- Prev/Next pagination controls with "1–4 of 6" counter

**Run detail slide-out panel:**
- Clicking any run row (latest or history) opens a slide-out panel on the right (300px wide, fixed position)
- Panel slides in from the right with `--duration-normal` and `--ease-out`
- Shows: StatusBadge, run summary, metadata grid (Run ID, Started, Duration, Tokens, Tool calls)
- "View reasoning timeline" button → navigates to `/runs/:id`
- Close button (×) dismisses the panel

**Empty state:**
When no policies exist, show a purposeful empty state — not just "No policies yet":
- Centered content with a subtle illustration or icon (interlocking links motif)
- Headline: "No policies yet"
- Subtext: "Create your first policy to start running agents"
- Three-step visual hint: "1. Create a policy → 2. Assign tools → 3. Trigger a run"
- Prominent "Create Policy" CTA button

**Keyboard navigation:**
- `j` / `k` — move selection highlight between folder rows and policy rows
- `Enter` — expand/collapse the selected folder, or open the slide-out for the selected run
- `Esc` — close the slide-out panel
- `n` — navigate to `/policies/new` (new policy)

**Live updates (SSE):**
- `run.status_changed` → update the affected policy's latest run status, folder status dot, stats bar
- `run.step_added` → update token cost if changed

**Acceptance criteria:**
- Skeleton screens shown on initial load, no spinners
- Policies grouped by folder with collapsible folder rows (animated)
- Folder status dot reflects worst-case status, transitions smoothly on change
- Policy rows show latest run with status, timing, tokens
- Run history expandable per policy with pagination
- Run detail slide-out panel on row click (animated)
- Stats bar reflects current state with large (32px) counter numbers
- "New Policy" button navigates to `/policies/new`
- Empty state renders with CTA when no policies exist
- `j`/`k`/`Enter`/`Esc`/`n` keyboard shortcuts work

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
- Save button: active (blue) when dirty, muted when clean. Label: "Save Policy" / "Saved"
- `Cmd+S` / `Ctrl+S` saves the policy (prevent browser default save)

#### YAML Mode

- Full-height CodeMirror 6 editor (`@codemirror/lang-yaml`), ~30KB gzipped
- Syntax validation indicator: "● Valid YAML" (green) or "● Invalid YAML" (red) with error message
- Malformed YAML blocks the save button
- On save error from API (validation failure): display error inline below the editor, do not navigate away
- Helper text above editor: "Editing raw YAML — changes here sync to the form view on switch."

#### Form Mode

Two-column layout: main form (left, scrollable) + context sidebar (right, fixed 260px).

**Main form sections:**

1. **Policy Identity**
   - Name field (monospace input)
   - Description field
   - Folder field (text input, optional — for dashboard grouping)

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
     - For actuators: approval toggle switch (amber when enabled, shows "approval req." / "no approval")
     - Remove button (×)
   - "Add tool from registry" button → opens inline search panel:
     - Search input (filters by tool name, server name, or description)
     - Results list showing RoleBadge + `server.tool_name` + description
     - Clicking a result adds it to the capabilities list with a brief slide-in animation
     - Cancel button closes the search panel
   - Empty state: "No tools added yet. Add tools from the registry below."

4. **Task Instructions**
   - Multiline textarea for agent task prompt
   - Helper text: "The trigger payload (webhook body, poll result) is delivered as the agent's first message — reference it as needed."

5. **Run Limits**
   - Two-column grid: max tokens per run (number input) + max tool calls per run (number input)

6. **Concurrency Behaviour**
   - Four option cards in a grid: Skip / Queue / Parallel / Replace
   - Each card has a label and short description
   - Selected card has blue border + background tint

**Context sidebar (right panel, 260px):**

1. **Capability Envelope** — three large counters:
   - Sensors count (blue)
   - Actuators count (orange)
   - Gated count (amber) — actuators with approval required
   - If any actuators are gated, show a warning box listing them: "⚠ server.tool requires approval"

2. **Run Limits** — monospace summary:
   - `20,000 tokens`
   - `50 tool calls`
   - `skip concurrency`

3. **Quick Actions** — button list:
   - Test trigger (▶)
   - View run history (↗)
   - Duplicate policy (⎘)
   - Delete policy (✕, red text) — with confirmation modal

**Syncing between modes:**
- Switching from Form → YAML: serialize form state to YAML string
- Switching from YAML → Form: parse YAML string into form fields. If YAML is malformed, stay in YAML mode and show an error.

**New policy flow:**
- Pre-populate with default YAML template (same as before)
- Form mode starts with empty fields matching the template

**Webhook URL display:**
- In form mode: shown below the webhook trigger card
- In YAML mode: shown in a banner above the editor (only when trigger type is webhook)
- Copy-to-clipboard button on both

**Acceptance criteria:**
- Dual-mode toggle works, data syncs between modes
- YAML round-trips correctly: save → reload produces the same YAML
- Form mode: all fields editable, tool picker works, approval toggles work
- Capability envelope sidebar updates as tools are added/removed
- Syntax error in YAML mode blocks save
- API validation error shown inline, does not navigate away
- Webhook URL is visible and copyable after save
- Dirty indicator shows when unsaved changes exist
- `Cmd+S` saves the policy

---

### 4. Run Detail — Reasoning Timeline

**Reference mockup:** `gleipnir-reasoning-timeline.jsx`

**Endpoints:**
- `GET /api/v1/runs/:id` — run metadata
- `GET /api/v1/runs/:id/steps` — all steps

**Layout:** Full-page view with a back button ("← Back to dashboard"), run metadata header, filter chips, and a chronological step timeline.

#### Loading State

On initial load, show skeleton screens: a skeleton header block + 4-5 skeleton step cards matching the timeline layout. Steps use alternating widths to suggest varying content lengths.

#### Run Header

- Back button → navigate to `/dashboard` (also: `Esc` key navigates back)
- Policy name (linked to `/policies/:id`) + TriggerChip
- StatusBadge (large)
- Metadata grid: Run ID (truncated ULID), Started (absolute time), Duration (or "in progress" with elapsed timer), Tokens (total), Tool calls (count)
- If status is `failed` or `interrupted`: error message displayed in a red-bordered box
- Trigger payload: collapsible JSON block using `CollapsibleJSON` component

#### Filter Chips

Row of filter buttons: All | Thoughts | Calls | Results | Approvals | Errors

Each chip shows a count badge. Clicking a chip filters the timeline to only show steps of that type. "All" shows everything. `capability_snapshot` is never included in filter counts — it's infrastructure, not agent reasoning (ADR-018).

#### Step Timeline

Vertical timeline with a connector line between steps. Each step has an icon on the left (circles for thoughts/complete, rounded squares for tool calls/results) and a content card on the right.

New step cards enter with a slide-in animation (translate from -6px + fade in, `--duration-normal`, `--ease-spring`).

**Step type rendering:**

| Type | Icon | Content |
|---|---|---|
| `capability_snapshot` | Shield icon | Collapsed by default. Summary: "capability snapshot — N tools". Expand to show tool list with name, RoleChip, approval badge, presented schema with enum constraints highlighted. Always renders at the bottom of the timeline (step 0). |
| `thought` | Gray dot (`·`) | Text block, agent reasoning. Token cost shown. **For in-progress streaming:** render text character-by-character with a typing effect (30ms per character batch) and a blinking cursor (`LiveCursor` component) at the end. Cursor disappears when the step is complete. |
| `tool_call` | Blue `→` (sensor) or Orange `→` (actuator) | Tool name (monospace, colored by role) + RoleChip. Input JSON in a `CollapsibleJSON` block. |
| `tool_result` | Green `←` (success) or Red `←` (error) | Output in a `<pre>` block with `CopyBlock` wrapper. If `is_error: true`, red border and error styling. |
| `approval_request` | Amber shield | [v0.1 placeholder] Show "Approval requested — approval UI available in v0.2". [v0.2] Full inline approval card (see section 7). |
| `feedback_request` | Purple speech bubble | [v0.1 placeholder] Show "Feedback requested — feedback UI available in v0.2". |
| `feedback_response` | Purple speech bubble (filled) | [v0.1 placeholder] Show "Feedback response received." |
| `error` | Red `!` | Error message in a red-bordered box. |
| `complete` | Green checkmark | Summary text in a green-bordered success card. Token cost shown. |

**Live updates (SSE):**
- `run.step_added` → append new step card with slide-in animation. Auto-scroll to bottom if user is within 200px of the bottom. If user has scrolled up, show a "New steps ↓" pill at the bottom that scrolls down on click.
- `run.status_changed` → update header (status badge, duration, token cost). If status transitions to terminal, stop showing streaming indicators (remove LiveCursor, stop elapsed timer).

**Keyboard navigation:**
- `Esc` — navigate back to dashboard
- `j` / `k` — move between steps
- `Enter` — expand/collapse the selected step's JSON or details
- `c` — copy the selected step's content to clipboard

**Acceptance criteria:**
- Skeleton screens on initial load
- All step types render without crashing
- Live runs append new steps via SSE with slide-in animation
- Thought steps stream text character-by-character with blinking cursor for in-progress runs
- JSON blobs in tool_call/tool_result are collapsible with copy buttons
- Token cost per step is visible
- Filter chips work and show correct counts
- Capability snapshot renders at the bottom, collapsed by default
- Back button and `Esc` return to dashboard
- "New steps ↓" pill appears when user has scrolled away from bottom during live run
- `j`/`k`/`Enter`/`c` keyboard shortcuts work

---

### 5. MCP Server Management

**Reference mockup:** `gleipnir-mcp-registry.jsx`

**Endpoints:**
- `GET /api/v1/mcp/servers` — list registered servers
- `POST /api/v1/mcp/servers` — register new server
- `DELETE /api/v1/mcp/servers/:id` — remove server
- `POST /api/v1/mcp/servers/:id/discover` — trigger re-discovery (returns diff)
- `GET /api/v1/mcp/servers/:id/tools` — list tools with capability roles
- `PATCH /api/v1/mcp/tools/:id` — update capability role

**Layout:** Full-page view with a top section for global stats and actions, followed by server cards.

#### Loading State

Skeleton screens: stat counters as skeleton blocks + 2-3 skeleton server cards.

#### Global Stats and Actions

- Total tools count, sensors count, actuators count, unassigned count
- "Add Server" button → opens modal

#### Add Server Modal

- Slides in with `--duration-normal` and `--ease-out`
- Fields: Name (text input) + URL (text input, monospace, placeholder: `http://my-server:8080`)
- Cancel / Add Server buttons
- On success: server card appears in the list with a slide-in animation, auto-triggers discovery

#### Server Cards

One card per registered MCP server:

**Card header:**
- Health dot (pulsing green = reachable, solid red = unreachable, pulsing amber = checking)
- Server name
- Server URL (monospace, muted)
- Last discovered timestamp (relative: "3h ago") or "Never discovered"
- "Discover" button (triggers re-discovery, shows spinner while running)
- Expand/collapse chevron for tool list
- Delete button (trash icon) — with confirmation modal ("This will remove N tools from the registry. Affected policies: ...")

**Unassigned role warning:**
If any tools on the server have no capability role assigned, show an amber banner: "N tools need a capability role assigned before they can be used in policies"

**Tool list (per server, expandable):**

Expand/collapse animated with smooth height transition.

Each tool row (grid layout):
- Tool name (`server.tool_name`, monospace)
- Description
- **Policy cross-references** — list of policy names that use this tool, with scoping indicator:
  - "worker-pod-watcher (scoped)" — policy uses this tool with param constraints
  - "daily-cluster-digest" — policy uses this tool without scoping
  - Tools not used in any policy show "not used" in muted text
- Input schema: expandable `CollapsibleJSON` block showing parameter types, required flags, and which params are scopeable
- Capability role: **inline dropdown** to change role (`sensor` | `actuator` | `feedback`). Dropdown is color-coded by selected role. Saves on change via `PATCH /api/v1/mcp/tools/:id`.
- Expand button for full detail

#### Discovery Diff View

When re-discovery completes and tools have changed, show a diff overlay/section:

- **Added tools** — green left-border, tool name + description + suggested role dropdown
- **Removed tools** — red left-border, tool name + description, strikethrough
- **Modified tools** — amber left-border, tool name + old description → new description

Each section has an "Accept" action. Added tools need a role assignment before accept. The diff view shows affected policies that reference changed/removed tools.

**Acceptance criteria:**
- Skeleton screens on initial load
- Servers list with health indicator and last discovery time
- Add server modal works (animated), auto-discovers on creation
- Re-discovery shows diff when tools change (added/removed/modified)
- Tool capability roles are editable via inline dropdown
- Policy cross-references shown per tool
- Unassigned role warning banner when applicable
- Delete server works with confirmation showing affected policies
- Adding and removing servers works

---

## v0.2 — Approval UI

> Build this milestone after the Go approval gate backend (EPIC-006) is complete and the approval endpoints are available. Do not stub approval UI into v0.1 beyond the placeholder cards noted above.

### 6. Dashboard — Pending Approvals Section

**Reference mockup:** `gleipnir-dashboard.jsx` (ApprovalsSection, ApprovalCard)

**Endpoint:** `GET /api/v1/approvals?status=pending`

Replaces the v0.1 placeholder banner with full interactive approval cards.

**Section header:**
- Pulsing amber dot + "Pending Approvals" + count badge
- When all resolved: "— all resolved" (muted)

**Approval card (one per pending approval):**

- Top amber gradient bar (3px, visual accent)
- **Header row:**
  - Amber alert icon (circle with exclamation)
  - Policy name + folder label + TriggerChip
  - Agent summary (1-2 sentence description of what the agent wants to do)
  - Countdown timer showing time remaining before timeout (`expires_at`). Timer behavior escalates as time runs out:
    - Normal: amber text, static display
    - < 5 minutes: timer turns red, border pulses subtly
    - < 1 minute: entire card border glows amber with a slow pulse animation (`box-shadow` transition)
  - "Show reasoning" / "Hide reasoning" toggle

- **Proposed action block** (always visible):
  - "PROPOSED ACTION" label
  - Tool name in an orange monospace badge
  - "actuator · approval required" label (muted)
  - Proposed input as formatted JSON using `CollapsibleJSON`

- **Agent reasoning trace** (collapsible, hidden by default):
  - "AGENT REASONING" label
  - Mini reasoning timeline showing the steps that led to this approval request (thoughts, tool calls, tool results)
  - Same visual style as the full reasoning timeline but compact
  - Expand/collapse animated with smooth height transition and subtle backdrop blur behind the trace when expanded

- **Action row:**
  - "Started {time} · run paused, waiting for your decision" (muted)
  - Reject button (red outline)
  - Approve button (green outline)

- **Confirm flow** (after clicking Approve or Reject):
  - Replaces the action row with a confirmation area (animated transition)
  - Optional note textarea (placeholder varies by action)
  - Cancel button + "Confirm Approve" / "Confirm Reject" button

**Approval receipt (after decision):**
- Replaces the approval card with a compact receipt row
- Status dot transitions: amber → green (approved) or amber → red (rejected), animated with `--duration-normal`
- Checkmark (approved, green) or X (rejected, red) + "Approved"/"Rejected" + policy name + tool name
- If note was provided: shows note in italics
- "confirming…" spinner until SSE confirms the status change
- After confirmation: receipt fades out over `--duration-slow`, then card smoothly collapses (height → 0)

**Keyboard navigation:**
- `a` — approve the first pending approval (opens confirm flow)
- `r` — reject the first pending approval (opens confirm flow)
- `Tab` — cycle between pending approval cards

**Live updates (SSE):**
- `approval.created` → new approval card slides in from the top
- `approval.resolved` → card transitions to receipt, then fades and collapses

**Acceptance criteria:**
- All pending approvals visible with countdown timers
- Countdown timer escalation (color change, pulse, glow) at 5min and 1min thresholds
- Agent reasoning trace expandable per approval with backdrop blur
- Approve/reject actions require confirmation with optional note
- Receipts show after decision with status dot color transition, fade out after confirmation
- SSE events update approval state in real time
- `a`/`r`/`Tab` keyboard shortcuts work

### 7. Run Detail — Approval Step Cards

Upgrade the placeholder `approval_request` step cards from v0.1:

**Reference mockup:** `gleipnir-reasoning-timeline.jsx` (approval_request step type)

- Show tool name (orange monospace badge) + RoleChip ("actuator")
- Proposed input as formatted JSON with `CopyBlock`
- Countdown timer if status is `pending` (same escalation behavior as dashboard approval cards)
- If status is `pending`: show Approve / Reject buttons inline in the timeline, with the same confirm flow as the dashboard (optional note → confirm)
- If status is `approved` / `rejected` / `timeout`: show decided state with green checkmark / red X / amber clock icon, timestamp, and note if present
- "Jump to approval" button that scrolls to the approval step when viewing the dashboard

### 8. Feedback Round-Trip

Upgrade `feedback_request` and `feedback_response` step cards:

- `feedback_request`: show the agent's message in a purple-bordered card. If awaiting response, show a text input + "Send Response" button
- `feedback_response`: show the operator's response in a visually distinct style (right-aligned or different background color)

---

## v0.1-polish — Design Quality Pass

> This section covers polish items that elevate the UI from functional to production-quality. These can be worked on in parallel with v0.1 features or immediately after. They are not blockers for v0.1 launch but should ship before the UI is shown externally.

### 9. Branding and Identity

**The GLEIPNIR wordmark and logo need a custom design.** The current placeholder is a clock-like SVG that has no connection to the product's identity. Gleipnir is named after the Norse mythological binding — "smooth as silk, stronger than iron, invisible in its constraint."

- Commission or design a logo mark: interlocking links, a stylized knot, or an abstract binding pattern. The mark should work at 16×16 (favicon) and 32×32 (top bar) sizes.
- Wordmark: consider a custom letterform or ligature in the "GLEIPNIR" text. At minimum, use `--weight-bold` (700) with tight letter-spacing.
- Favicon: derived from the logo mark
- The logo should evoke constraint and elegance, not surveillance or timekeeping

### 10. Empty, Error, and Edge States

Every view needs designed states beyond the happy path:

**Empty states (per view):**
- Dashboard (no policies): covered in section 2
- Policy editor (no tools added): covered in section 3
- MCP servers (no servers registered): "No MCP servers registered. Add a server to discover available tools." with "Add Server" CTA
- MCP server (no tools discovered): "No tools discovered. Click Discover to scan this server."
- Run history (no runs for a policy): "No runs yet. This policy hasn't been triggered."

**Error states:**
- API unreachable on page load: full-page error with retry button. Show the last successful data if cached.
- SSE disconnect: `ConnectionBanner` (covered in section 1). Non-blocking — the page remains usable with stale data.
- Individual request failure (e.g., save policy fails): inline error message at the point of action, not a toast or global error

**Edge states:**
- Very long policy names: truncate with ellipsis in table cells, show full name on hover (tooltip)
- Very long tool names: same truncation pattern
- Many tools on one server (20+): virtual scrolling or "Show all N tools" with initial limit of 10
- Many folders (10+): all render, no virtual scrolling needed at this scale
- Run with 100+ steps: virtual scrolling on the timeline (only render steps in viewport + buffer)

### 11. Keyboard Shortcut Discoverability

- `?` key opens a keyboard shortcut overlay listing all available shortcuts grouped by view
- Bottom-right corner of every page shows a subtle hint: `? shortcuts` in `--text-muted` color
- The overlay is a centered modal with two columns (shortcut key + description), grouped by context (Global, Dashboard, Timeline, Editor)

**Global shortcuts (available everywhere):**
| Key | Action |
|---|---|
| `Cmd+K` / `Ctrl+K` | Command palette |
| `?` | Keyboard shortcut overlay |
| `g d` | Go to dashboard |
| `g s` | Go to servers |
| `g n` | Go to new policy |

**Dashboard shortcuts:**
| Key | Action |
|---|---|
| `j` / `k` | Navigate rows |
| `Enter` | Expand / open selected |
| `Esc` | Close slide-out |
| `n` | New policy |

**Timeline shortcuts:**
| Key | Action |
|---|---|
| `j` / `k` | Navigate steps |
| `Enter` | Expand/collapse step details |
| `c` | Copy step content |
| `Esc` | Back to dashboard |

**Editor shortcuts:**
| Key | Action |
|---|---|
| `Cmd+S` | Save policy |

### 12. Transition and Animation Polish

Audit all transitions for consistency with the motion system:

- **Expand/collapse** (folders, tool lists, JSON blocks, reasoning traces): smooth height animation using `max-height` + `overflow: hidden`, `--duration-normal`, `--ease-out`
- **Slide-out panel** (dashboard run detail): translate-x from right, `--duration-normal`, `--ease-out` on enter, `--ease-in` on exit
- **Step card entry** (timeline): translate-y from -6px + opacity 0→1, `--duration-normal`, `--ease-spring`
- **List stagger**: first render of folder rows, tool rows, and step cards stagger by 30ms × index (max 10 items)
- **Status dot color change**: `transition: background var(--duration-normal) ease` — no hard cuts when SSE updates change a folder's status
- **Approval receipt fade-out**: opacity 1→0 over `--duration-slow`, followed by height collapse
- **Modal enter/exit**: fade-in backdrop + slide-up content (`--duration-normal`, `--ease-out`)
- **Hover states**: all interactive elements have a hover transition on `--duration-fast`

### 13. Streaming Text Effect for Thoughts

When a `thought` step is being written in real time (via SSE `run.step_added` where the step is still streaming):

- Render the text progressively, batch-appending characters at ~30ms intervals
- Show a `LiveCursor` (blinking blue rectangle, 7×13px) at the insertion point
- The cursor blinks at 1.1s interval (`animation: cursorBlink 1.1s ease-in-out infinite`)
- When the thought is complete (next step arrives or run reaches terminal state), remove the cursor immediately — no fade, just gone
- For already-complete thoughts (loaded from API, not streaming), render the full text with no cursor

This creates a visceral sense of watching the agent think and is the single most distinctive micro-interaction in the UI.

### 14. Theme and Accessibility Foundation

#### Theme Architecture

All visual theming runs through CSS custom properties on `:root`, selected by a `data-theme` attribute on `<html>`. This single mechanism supports dark/light mode AND color blindness schemes.

- All color references go through CSS custom properties — no hard-coded hex values in component CSS. This is already enforced by the "no inline styles" rule.
- Create `themes/dark.css` that sets all `:root` variables (this is the current default)
- Create empty `themes/light.css` placeholder with a comment: "Override dark theme variables here"
- Create `themes/cb-deuteranopia.css` — color blindness scheme for red-green deficiency (see below)
- Create `themes/cb-tritanopia.css` — color blindness scheme for blue-yellow deficiency (see below)
- Add `data-theme="dark"` attribute on `<html>` that the theme CSS selectors target
- Theme selector: a small dropdown in the top bar (next to the GLEIPNIR wordmark or in a settings area) offering: Dark (default) | Light | Deuteranopia | Tritanopia
- Store the selection in `localStorage` and apply on page load before first paint (inline `<script>` in `index.html` to prevent flash)
- Color blindness themes layer on top of dark/light — they are composable. E.g., `data-theme="dark cb-deuteranopia"` applies the dark background palette with deuteranopia-safe semantic colors.

#### Color Blindness Schemes

The default palette uses red/green as opposing signals (failed/success, reject/approve, removed/added). This is indistinguishable for ~8% of males with deuteranopia or protanopia. The fix is two-part: alternative palettes AND a structural rule that color is never the only differentiator.

**Deuteranopia / Protanopia scheme** (red-green deficiency, most common):

```css
[data-theme~="cb-deuteranopia"] {
  --color-green:   #56B4E9;  /* Sky blue — replaces green for success/complete */
  --color-red:     #D55E00;  /* Vermillion — replaces red for failed/error */
  --color-amber:   #E69F00;  /* Orange-yellow — approvals/warnings (unchanged, already safe) */
  --color-blue:    #0072B2;  /* Darker blue — sensors/running (shifted to avoid green-blue confusion) */
  --color-orange:  #CC79A7;  /* Rose pink — actuators (shifted off orange to avoid red confusion) */
  --color-purple:  #9467BD;  /* Kept similar — feedback/interrupted */
  --color-teal:    #009E73;  /* Bluish green — poll triggers */
}
```

These values are derived from the [Bang Wong color palette](https://www.nature.com/articles/nmeth.1618), which is designed for universal accessibility in scientific visualization.

**Tritanopia scheme** (blue-yellow deficiency, rare):

```css
[data-theme~="cb-tritanopia"] {
  --color-green:   #009E73;  /* Teal-green — replaces green (safe for tritanopia) */
  --color-red:     #D55E00;  /* Vermillion — replaces red */
  --color-amber:   #CC79A7;  /* Rose pink — replaces amber (yellow is problematic) */
  --color-blue:    #56B4E9;  /* Sky blue — sensors/running */
  --color-orange:  #E69F00;  /* Orange — actuators */
  --color-purple:  #882255;  /* Deep magenta — feedback/interrupted */
  --color-teal:    #117733;  /* Forest green — poll triggers */
}
```

#### "Never Color Alone" Rule

**Every piece of information conveyed by color must also be conveyed by shape, icon, or text.** This is a hard rule, not a guideline. Audit every color-dependent element:

| Element | Color signal | Non-color redundancy required |
|---|---|---|
| StatusBadge | Green/blue/amber/red/purple dot | ✅ Already has text label ("Complete", "Failed", etc.) |
| TriggerChip | Blue/purple/teal text | ✅ Already has text label ("webhook", "cron", "poll") |
| RoleChip | Blue/orange/purple text | ✅ Already has text label ("sensor", "actuator", "feedback") |
| Folder status dot | Color-only | ❌ **Add tooltip on hover showing status text** |
| Health dot (MCP) | Green/red/amber | ❌ **Add text label: "reachable" / "unreachable" / "checking"** |
| Discovery diff borders | Green/red/amber left-border | ❌ **Add prefix icon: `+` (added), `−` (removed), `~` (modified)** |
| Approve/Reject buttons | Green/red text and border | ✅ Already has text label, but **also add ✓ and ✕ icons** |
| Timeline step icons | Color varies by type | ✅ Already has distinct shapes (circle vs rounded square) and symbols (→, ←, ·, !, ✓) |
| Tool call icon color | Blue (sensor) vs orange (actuator) | ❌ **Add `S` or `A` letter inside the icon, or show RoleChip next to tool name** |

Items marked ❌ must be fixed before shipping. The non-color redundancy should be present in all themes, not just the color blindness schemes — it benefits everyone.

#### ARIA and Screen Reader Basics

Not a full WCAG audit, but establish the foundation:

- All interactive elements (`button`, `a`, custom clickable divs) must have `role` and `aria-label` attributes where the visual label is insufficient (e.g., icon-only buttons like the history toggle, close ×, expand chevron)
- Status changes delivered via SSE should update an `aria-live="polite"` region so screen readers announce "Run r502 status changed to complete"
- Approval countdown timers should have `aria-label` with the time remaining in words ("18 minutes remaining"), not just the visual `18:00`
- The command palette should follow the [WAI-ARIA combobox pattern](https://www.w3.org/WAI/ARIA/apg/patterns/combobox/)
- Focus management: opening a modal traps focus inside it; closing returns focus to the trigger element
- Keyboard shortcuts must not conflict with screen reader shortcuts — all shortcuts should be suppressible via a "keyboard shortcuts enabled" toggle in settings

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

This disables pulse animations, stagger effects, slide-ins, and the streaming text cursor blink. Content still appears — it just appears instantly instead of animating.

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
POST   /api/v1/mcp/servers/:id/discover   — returns discovery diff
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

**Discovery diff response shape** (from `POST /api/v1/mcp/servers/:id/discover`):
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
- Automatic MCP drift detection (v0.4) — v0.1 has manual re-discovery with diffs
- Multi-user / user identity UI (post v0.4)
- Policy dry-run mode
- Any agent-to-agent or multi-agent UI
- Full light theme implementation (v0.1-polish sets up the foundation and dark theme; light theme colors are a future task)
- Full WCAG 2.1 AA audit (v0.1-polish covers the structural foundation — ARIA, focus management, reduced motion, color blindness schemes — but a formal audit is deferred)
- Sound/audio notifications (consider for v0.3 with approval urgency)
