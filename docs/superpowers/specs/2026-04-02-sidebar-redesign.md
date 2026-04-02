# Sidebar Redesign: Status-at-a-Glance Navigation

**Date:** 2026-04-02
**Status:** Approved for implementation
**Scope:** Redesign sidebar footer, add nav-level status indicators for approvals and MCP health, move connection banner to content area, remove theme toggle from sidebar.

**Visual mockup:** `screenshots/sidebar-mockup.html` — open via `http://localhost:9999/sidebar-mockup.html` for reference (run `python3 -m http.server 9999` from `screenshots/` dir).

---

## 1. Overview

The current sidebar footer contains a ThemeToggle (3 icon buttons) and a ConnectionBanner ("Connected" green box). This is cluttered, wastes vertical space, and violates the design philosophy: "Respect operator time — show only what needs attention."

This redesign transforms the sidebar into a **status-at-a-glance** system where nav items themselves communicate system state through color and animation, the footer becomes a clean user identity/settings entry point, and the connection banner moves to the content area (only shown when disconnected).

**Design philosophy:** The sidebar is quiet infrastructure. Nav items are the primary concern. Status indicators are ambient — visible at a glance but not competing with navigation. Connected is the default state and should not be displayed. Only surface what needs attention.

---

## 2. Sidebar Footer: User Account

Replace the current footer contents (ThemeToggle + ConnectionBanner) with a user account row.

### 2.1 Expanded State

```
┌──────────────────────┐
│  [A]  admin       ⚙  │
│  ●    Admin           │
└──────────────────────┘
```

- **User avatar:** 28×28px circle, `border-radius: 50%`. Background: `color-mix(in srgb, var(--color-blue) 20%, transparent)`. Text: user's first initial (uppercased), `var(--color-blue)`, `font-size: 12px`, `font-weight: 600`.
- **Online dot:** 8×8px green circle (`var(--color-green)`) positioned absolute at bottom-right of avatar with a 2px `var(--bg-sidebar)` border. This is cosmetic — it indicates the session is active, not SSE connection.
- **User info:** Two lines. Name: `font-size: var(--text-sm)`, `font-weight: var(--weight-medium)`, `var(--text-primary)`. Role: `font-size: var(--text-xs)`, `var(--text-muted)`. Both lines truncate with ellipsis.
- **Settings gear icon:** Lucide `Settings` icon, 16px, `var(--text-faint)`. On row hover, changes to `var(--text-muted)`.
- **The entire row is clickable** — navigates to `/settings`. Hover state: `background: var(--bg-elevated)`.
- **Footer container:** `padding: 12px`, `border-top: 1px solid var(--border-subtle)`, `cursor: pointer`, `transition: background var(--duration-fast) var(--ease-out)`.

### 2.2 Collapsed State

- Show only the avatar circle, centered, 24×24px variant (smaller).
- Online dot scales to 6px with 1.5px border.
- No text, no gear icon.
- Clickable — same `/settings` navigation.
- `padding: 12px 0`, `justify-content: center`.

### 2.3 Data Source

Use the existing `useCurrentUser()` hook which returns `{ username, roles }`. Display the first role as the subtitle (capitalize first letter). Extract the first character of `username` for the avatar initial.

---

## 3. Nav-Level Status Indicators

### 3.1 Control Center — Pending Approvals (Amber Pulse)

When there are pending approval or feedback requests, the Control Center nav item changes:

- **Icon + label color:** `var(--color-amber)` (#F59E0B) instead of the normal muted/active color
- **Active accent bar (::before):** `var(--color-amber)` instead of `var(--color-blue)`
- **Active background:** `color-mix(in srgb, var(--color-amber) 8%, transparent)` instead of blue tint
- **Animation:** `animation: statusPulse 2s ease-in-out infinite` — opacity breathes between 1.0 and 0.4
- **Collapsed mode:** Same amber color + pulse on the icon alone. Tooltip: "N items need attention"

**Data source:** Use the existing `useAttentionItems()` hook (already in `src/hooks/useAttentionItems.ts`). If `attentionItems.length > 0`, apply the amber state. This covers both approval requests and feedback requests.

**When no pending items:** Normal nav styling. No animation, no color override. Completely silent.

### 3.2 Tools — MCP Server Health (Orange Pulse)

When any MCP server is unhealthy (never successfully discovered), the Tools nav item changes:

- **Icon + label color:** `var(--color-orange)` (#FB923C)
- **Animation:** Same `statusPulse` keyframes as above
- **Collapsed mode:** Same orange color + pulse on the icon. Tooltip includes count: "N MCP server(s) unreachable"

**Data source:** Use the existing `useMcpServers()` hook. Check `servers.some(s => s.last_discovered_at === null)`. This is the same logic currently in `McpHealthDot.tsx`.

**When all healthy (or no servers configured):** Normal nav styling.

### 3.3 CSS Implementation

Add these CSS classes to `Layout.module.css`:

```css
/* Status pulse animation — shared by all nav status indicators */
@keyframes statusPulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.4; }
}

/* Pending approvals — amber override */
.navLinkNeedsApproval {
  color: var(--color-amber);
  animation: statusPulse 2s ease-in-out infinite;
}

.navLinkNeedsApproval .navIcon svg { /* target via parent — or use a wrapper class */ }

/* Override active state when needs-approval */
.navLinkActive.navLinkNeedsApproval {
  background: color-mix(in srgb, var(--color-amber) 8%, transparent);
}

.navLinkActive.navLinkNeedsApproval::before {
  background: var(--color-amber);
}

/* MCP unhealthy — orange override */
.navLinkMcpUnhealthy {
  color: var(--color-orange);
  animation: statusPulse 2s ease-in-out infinite;
}

.navLinkActive.navLinkMcpUnhealthy {
  background: color-mix(in srgb, var(--color-orange) 8%, transparent);
}

.navLinkActive.navLinkMcpUnhealthy::before {
  background: var(--color-orange);
}
```

Respect `prefers-reduced-motion`:
```css
@media (prefers-reduced-motion: reduce) {
  .navLinkNeedsApproval,
  .navLinkMcpUnhealthy {
    animation: none;
  }
}
```

### 3.4 Layout.tsx Changes

In the NAV_ITEMS map, conditionally apply status classes:

```tsx
// In Layout component body:
const { data: attentionItems } = useAttentionItems()
const { data: mcpServers } = useMcpServers()

const hasPendingApprovals = (attentionItems?.length ?? 0) > 0
const hasUnhealthyServers = mcpServers?.some(s => s.last_discovered_at === null) ?? false
```

When rendering nav links, for the `/dashboard` route, add `styles.navLinkNeedsApproval` if `hasPendingApprovals`. For the `/tools` route, add `styles.navLinkMcpUnhealthy` if `hasUnhealthyServers`.

---

## 4. Connection Banner — Content Area Only

### 4.1 Remove from Sidebar

Remove `<ConnectionBanner>` from the sidebar footer entirely. Delete the import of `ConnectionBanner` from `Layout.tsx` sidebar section.

### 4.2 Add to Content Area

Render the connection banner **above the main content** inside `.mainWrapper`, only when `connectionState !== 'connected'`:

```tsx
<div className={styles.mainWrapper}>
  {connectionState !== 'connected' && (
    <div className={styles.disconnectBanner} role="status">
      <span className={styles.disconnectDot} aria-hidden="true" />
      {connectionState === 'reconnecting'
        ? 'Connection lost — reconnecting…'
        : 'Connection lost'}
    </div>
  )}
  <main className={styles.main}>
    ...
  </main>
</div>
```

### 4.3 Disconnect Banner Styling

```css
.disconnectBanner {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-4);
  background: color-mix(in srgb, var(--color-amber) 12%, transparent);
  border-bottom: 1px solid color-mix(in srgb, var(--color-amber) 25%, transparent);
  color: var(--color-amber);
  font-size: var(--text-sm);
  font-weight: var(--weight-medium);
  animation: slideDown var(--duration-normal) var(--ease-out);
}

.disconnectDot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--color-amber);
  flex-shrink: 0;
  animation: gPulse 1.2s ease-in-out infinite;
}

@keyframes slideDown {
  from { transform: translateY(-100%); opacity: 0; }
  to { transform: translateY(0); opacity: 1; }
}
```

When `connectionState === 'disconnected'` (not reconnecting), use `var(--color-red)` instead of amber, and no pulse on the dot.

### 4.4 No "Connected" State

When connected, render nothing. No banner, no indicator. Connected is the default.

---

## 5. Remove ThemeToggle from Sidebar

### 5.1 Delete from Layout

Remove `<ThemeToggle>` from the sidebar footer. Remove the import.

### 5.2 Theme Toggle Future Home

The ThemeToggle will live on a future `/settings` page. For now, the theme preference persists in localStorage (the `useTheme` hook still works). Users who have previously set a theme keep their preference. The setting just isn't accessible from the sidebar anymore.

**Do NOT delete the ThemeToggle component or useTheme hook** — they will be used on the settings page in a future PR.

---

## 6. Delete McpHealthDot Component

The `McpHealthDot.tsx` and `McpHealthDot.module.css` components are fully replaced by the nav-level orange pulse. Delete both files. Remove the import from `Layout.tsx`.

---

## 7. Files to Modify

| File | Action |
|------|--------|
| `frontend/src/components/Layout/Layout.tsx` | Major — new footer, status classes, disconnect banner in content area, remove ThemeToggle/ConnectionBanner/McpHealthDot imports |
| `frontend/src/components/Layout/Layout.module.css` | Major — new footer styles, status pulse classes, disconnect banner styles, remove old `.sidebarFooter` column layout |
| `frontend/src/components/Layout/McpHealthDot.tsx` | Delete |
| `frontend/src/components/Layout/McpHealthDot.module.css` | Delete |
| `frontend/src/components/Layout/Layout.test.tsx` | Update — tests for new footer, status indicators, disconnect banner placement |
| `frontend/src/components/Layout/Layout.stories.tsx` | Update — stories showing normal, approval-pending, mcp-unhealthy, disconnected states |

### Files NOT to modify

- `ThemeToggle/` — keep as-is, will be used on settings page later
- `ConnectionBanner/` — keep as-is for now (the component can be deleted in a cleanup PR, or repurposed for the content-area banner if desired; the Layout can inline the simple disconnect banner markup directly)
- `useSSE.ts`, `useAttentionItems.ts`, `useMcpServers.ts` — no changes needed, these hooks already provide the data

---

## 8. Test Strategy

### Unit Tests (Layout.test.tsx)

1. **Footer renders user info:** Mock `useCurrentUser` to return `{ username: 'alice', roles: ['admin'] }`. Assert avatar shows "A", name shows "alice", role shows "Admin".
2. **Footer navigates to /settings:** Click the footer row, assert navigation to `/settings`.
3. **Collapsed footer shows avatar only:** Set collapsed state, assert username/role/gear are hidden.
4. **Approval pulse applies:** Mock `useAttentionItems` to return 2 items. Assert Control Center nav link has the `navLinkNeedsApproval` class.
5. **Approval pulse absent when empty:** Mock `useAttentionItems` to return []. Assert class is not present.
6. **MCP unhealthy pulse applies:** Mock `useMcpServers` to return a server with `last_discovered_at: null`. Assert Tools nav link has `navLinkMcpUnhealthy` class.
7. **MCP pulse absent when healthy:** All servers have `last_discovered_at` set. Assert class is not present.
8. **Disconnect banner shown when reconnecting:** Mock `useSSE` with `connectionState: 'reconnecting'`. Assert banner text is visible.
9. **Disconnect banner hidden when connected:** Mock `useSSE` with `connectionState: 'connected'`. Assert no banner rendered.
10. **McpHealthDot is not imported/rendered:** Verify the old component is gone.

### Storybook Stories (Layout.stories.tsx)

Stories showing the sidebar in various states:
- Default (no alerts, connected)
- With pending approvals (Control Center amber)
- With unhealthy MCP servers (Tools orange)
- Both alerts active simultaneously
- Disconnected state (banner visible)
- Collapsed variants of each

---

## 9. Accessibility

- Footer row: `role="button"`, `tabindex="0"`, keyboard-activatable (Enter/Space).
- Status pulse animation respects `prefers-reduced-motion: reduce` — animation disabled, colors remain.
- Disconnect banner: `role="status"` for screen reader announcement.
- Collapsed nav items retain `title` attributes with labels.
- Status tooltips on collapsed items include context: "Control Center — 3 items need attention", "Tools — 1 MCP server unreachable".
