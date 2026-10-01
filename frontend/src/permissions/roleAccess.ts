import type { Role } from '@/components/UsersPage/roles'

// ROUTE_ROLES is the one place the UI decides which roles may use which
// top-level page. Each entry mirrors the RequireRole gate on that page's
// primary read endpoint in internal/http/api/router.go — the server is the
// authority; this map only keeps the UI from offering a page whose first
// request would answer 403. Change the two together.
//
// `admin` is listed for readability only: auth.RequireRole lets an admin past
// every gate, and canAccess does the same. ANY_SIGNED_IN marks a page whose
// reads carry no role gate at all, so it is offered even before /auth/me has
// answered and to a user holding no role.
export const ANY_SIGNED_IN = 'any-signed-in'

export const ROUTE_ROLES = {
  // GET /api/v1/attention, /stats, /stats/timeseries — no RequireRole.
  '/dashboard':        ANY_SIGNED_IN,
  // GET /api/v1/runs
  '/runs':             ['admin', 'operator', 'approver', 'auditor'],
  // GET /api/v1/policies
  '/agents':           ['admin', 'operator', 'auditor'],
  // GET /api/v1/mcp/servers
  '/tools':            ['admin', 'operator', 'auditor'],
  // GET /api/v1/users
  '/admin/users':      ['admin'],
  // GET /api/v1/admin/models (admin sub-router)
  '/admin/models':     ['admin'],
  // GET /api/v1/admin/audiences — registered outside the admin sub-router so
  // operators can manage audiences and auditors can read them.
  '/admin/audiences':  ['admin', 'operator', 'auditor'],
  // GET /api/v1/admin/plugins (admin sub-router)
  '/admin/plugins':    ['admin'],
  // GET /api/v1/admin/settings, /admin/system-info (admin sub-router)
  '/admin/system':     ['admin'],
} as const satisfies Record<string, readonly Role[] | typeof ANY_SIGNED_IN>

export type AppRoute = keyof typeof ROUTE_ROLES

// SETUP_ROLES may act on the dashboard's setup checklist: configuring a model,
// a tool source and an agent are all admin|operator writes. The checklist's
// readiness reads are skipped for everyone else, since they would 403 for an
// approver and say nothing an auditor can act on.
export const SETUP_ROLES: readonly Role[] = ['admin', 'operator']

function hasAny(userRoles: readonly string[], allowed: readonly Role[]): boolean {
  if (userRoles.includes('admin')) return true
  return allowed.some(role => userRoles.includes(role))
}

export function canAccess(userRoles: readonly string[], route: AppRoute): boolean {
  const allowed: readonly Role[] | typeof ANY_SIGNED_IN = ROUTE_ROLES[route]
  if (allowed === ANY_SIGNED_IN) return true
  return hasAny(userRoles, allowed)
}

export function canSetUp(userRoles: readonly string[]): boolean {
  return hasAny(userRoles, SETUP_ROLES)
}
