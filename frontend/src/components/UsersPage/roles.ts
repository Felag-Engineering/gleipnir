export const ROLE_HIERARCHY = ['admin', 'operator', 'approver', 'auditor'] as const
export type Role = (typeof ROLE_HIERARCHY)[number]

// Cumulative capability list for each role — the permissions panel shows these as-is.
export const ROLE_CAPABILITIES: Record<Role, string[]> = {
  auditor: ['View runs, steps, and audit trail'],
  approver: ['View runs, steps, and audit trail', 'Approve or reject tool calls'],
  operator: [
    'View runs, steps, and audit trail',
    'Approve or reject tool calls',
    'Trigger runs, manage policies, respond to feedback',
  ],
  admin: [
    'View runs, steps, and audit trail',
    'Approve or reject tool calls',
    'Trigger runs, manage policies, respond to feedback',
    'Manage users, rotate secrets, configure system settings',
  ],
}

// Single-sentence summaries suitable for a `title` attribute.
export const ROLE_TOOLTIP: Record<Role, string> = {
  auditor: 'Auditor — view runs, steps, and the audit trail.',
  approver: 'Approver — everything an auditor can do, plus approve or reject tool calls.',
  operator:
    'Operator — everything an approver can do, plus trigger runs, manage policies, and respond to feedback.',
  admin: 'Admin — full access, including user management, secret rotation, and system settings.',
}

// Returns the new checked set: `role` plus every role of lower privilege
// (higher index in ROLE_HIERARCHY) added to `current`.
export function rolesWhenChecked(role: Role, current: Set<Role>): Set<Role> {
  const roleIndex = ROLE_HIERARCHY.indexOf(role)
  const next = new Set(current)
  for (let i = roleIndex; i < ROLE_HIERARCHY.length; i++) {
    next.add(ROLE_HIERARCHY[i])
  }
  return next
}

// Returns the new checked set: `role` and every role of lower privilege
// (higher index in ROLE_HIERARCHY) removed from `current`.
export function rolesWhenUnchecked(role: Role, current: Set<Role>): Set<Role> {
  const roleIndex = ROLE_HIERARCHY.indexOf(role)
  const next = new Set(current)
  for (let i = roleIndex; i < ROLE_HIERARCHY.length; i++) {
    next.delete(ROLE_HIERARCHY[i])
  }
  return next
}

// Returns the highest-privileged role in the set (lowest index in ROLE_HIERARCHY),
// or null when the set is empty. Drives the permissions panel display.
export function highestSelectedRole(current: Set<Role>): Role | null {
  for (const role of ROLE_HIERARCHY) {
    if (current.has(role)) return role
  }
  return null
}
