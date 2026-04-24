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

// Returns the role array to send to the API for a given radio selection:
// the selected role plus all lower-privilege roles (higher index in ROLE_HIERARCHY).
export function rolesForHighest(role: Role): Role[] {
  return [...ROLE_HIERARCHY.slice(ROLE_HIERARCHY.indexOf(role))]
}

// Returns the highest-privileged role from an API string array (lowest index in
// ROLE_HIERARCHY), or null when the array is empty. Used to drive the role badge
// and pre-populate the edit modal.
export function highestRoleFromArray(roles: string[]): Role | null {
  for (const role of ROLE_HIERARCHY) {
    if (roles.includes(role)) return role
  }
  return null
}
