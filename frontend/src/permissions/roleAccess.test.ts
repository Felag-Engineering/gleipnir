import { describe, it, expect } from 'vitest'
import { ROUTE_ROLES, canAccess, canSetUp, type AppRoute } from './roleAccess'

// Expected access per single role, written out from the RequireRole gates in
// internal/http/api/router.go rather than derived from ROUTE_ROLES, so a change
// to the map that drifts from the router fails here.
const EXPECTED: Record<string, AppRoute[]> = {
  admin: ['/dashboard', '/runs', '/agents', '/tools', '/admin/users', '/admin/models', '/admin/audiences', '/admin/plugins', '/admin/system'],
  operator: ['/dashboard', '/runs', '/agents', '/tools', '/admin/audiences'],
  approver: ['/dashboard', '/runs'],
  auditor: ['/dashboard', '/runs', '/agents', '/tools', '/admin/audiences'],
  none: ['/dashboard'],
}

const ROUTES = Object.keys(ROUTE_ROLES) as AppRoute[]

describe('canAccess', () => {
  it.each(Object.entries(EXPECTED))('%s reaches exactly its routes', (role, expected) => {
    const roles = role === 'none' ? [] : [role]
    expect(ROUTES.filter(r => canAccess(roles, r))).toEqual(expected)
  })

  it('takes the union over held roles', () => {
    expect(canAccess(['approver', 'auditor'], '/agents')).toBe(true)
    expect(canAccess(['approver'], '/agents')).toBe(false)
  })

  it('lets an admin through every gate, as auth.RequireRole does', () => {
    expect(ROUTES.every(r => canAccess(['admin'], r))).toBe(true)
  })
})

describe('canSetUp', () => {
  it.each([
    [['admin'], true],
    [['operator'], true],
    [['approver'], false],
    [['auditor'], false],
    [['approver', 'auditor'], false],
    [[], false],
  ] as const)('%j → %s', (roles, expected) => {
    expect(canSetUp(roles)).toBe(expected)
  })
})
