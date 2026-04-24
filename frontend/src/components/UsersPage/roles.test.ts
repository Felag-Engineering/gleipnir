import { describe, it, expect } from 'vitest'
import { rolesForHighest, highestRoleFromArray, type Role } from './roles'

describe('rolesForHighest', () => {
  it('admin yields all four roles', () => {
    expect(rolesForHighest('admin')).toEqual(['admin', 'operator', 'approver', 'auditor'])
  })

  it('operator yields operator, approver, auditor — not admin', () => {
    expect(rolesForHighest('operator')).toEqual(['operator', 'approver', 'auditor'])
  })

  it('approver yields approver and auditor', () => {
    expect(rolesForHighest('approver')).toEqual(['approver', 'auditor'])
  })

  it('auditor yields only auditor', () => {
    expect(rolesForHighest('auditor')).toEqual(['auditor'])
  })
})

describe('highestRoleFromArray', () => {
  it('returns null for an empty array', () => {
    expect(highestRoleFromArray([])).toBeNull()
  })

  it('returns admin when admin is present', () => {
    expect(highestRoleFromArray(['admin'])).toBe('admin')
  })

  it('returns admin when all roles are present', () => {
    expect(highestRoleFromArray(['admin', 'operator', 'approver', 'auditor'])).toBe('admin')
  })

  it('returns operator when operator is the highest', () => {
    expect(highestRoleFromArray(['operator', 'approver', 'auditor'])).toBe('operator')
  })

  it('returns approver when only approver and auditor are present', () => {
    expect(highestRoleFromArray(['approver', 'auditor'])).toBe('approver')
  })

  it('returns auditor when only auditor is present', () => {
    expect(highestRoleFromArray(['auditor'])).toBe('auditor')
  })

  it('is order-independent — returns highest regardless of array order', () => {
    expect(highestRoleFromArray(['auditor', 'admin', 'approver'])).toBe('admin')
  })

  it('ignores unknown role strings', () => {
    expect(highestRoleFromArray(['unknown', 'operator'] as Role[])).toBe('operator')
  })
})
