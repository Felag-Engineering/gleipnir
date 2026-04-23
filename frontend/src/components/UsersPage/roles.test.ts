import { describe, it, expect } from 'vitest'
import {
  rolesWhenChecked,
  rolesWhenUnchecked,
  highestSelectedRole,
  type Role,
} from './roles'

describe('rolesWhenChecked', () => {
  it('checking admin from empty set yields all four roles', () => {
    const result = rolesWhenChecked('admin', new Set<Role>())
    expect(result).toEqual(new Set(['admin', 'operator', 'approver', 'auditor']))
  })

  it('checking operator from empty set yields operator, approver, auditor — not admin', () => {
    const result = rolesWhenChecked('operator', new Set<Role>())
    expect(result).toEqual(new Set(['operator', 'approver', 'auditor']))
    expect(result.has('admin')).toBe(false)
  })

  it('checking approver from empty set yields approver and auditor', () => {
    const result = rolesWhenChecked('approver', new Set<Role>())
    expect(result).toEqual(new Set(['approver', 'auditor']))
  })

  it('checking auditor from empty set yields only auditor', () => {
    const result = rolesWhenChecked('auditor', new Set<Role>())
    expect(result).toEqual(new Set(['auditor']))
  })

  it('checking admin when admin is already present does not change the set', () => {
    const current = new Set<Role>(['admin', 'operator', 'approver', 'auditor'])
    const result = rolesWhenChecked('admin', current)
    expect(result).toEqual(current)
  })

  it('checking operator when only admin is present adds operator, approver, auditor', () => {
    const result = rolesWhenChecked('operator', new Set<Role>(['admin']))
    expect(result).toEqual(new Set(['admin', 'operator', 'approver', 'auditor']))
  })

  it('does not mutate the original set', () => {
    const original = new Set<Role>(['auditor'])
    rolesWhenChecked('admin', original)
    expect(original).toEqual(new Set(['auditor']))
  })
})

describe('rolesWhenUnchecked', () => {
  it('unchecking operator from full set yields only admin', () => {
    const full = new Set<Role>(['admin', 'operator', 'approver', 'auditor'])
    const result = rolesWhenUnchecked('operator', full)
    expect(result).toEqual(new Set(['admin']))
  })

  it('unchecking approver from full set yields admin and operator', () => {
    const full = new Set<Role>(['admin', 'operator', 'approver', 'auditor'])
    const result = rolesWhenUnchecked('approver', full)
    expect(result).toEqual(new Set(['admin', 'operator']))
  })

  it('unchecking admin from full set empties the set', () => {
    const full = new Set<Role>(['admin', 'operator', 'approver', 'auditor'])
    const result = rolesWhenUnchecked('admin', full)
    expect(result).toEqual(new Set())
  })

  it('unchecking auditor from full set removes only auditor', () => {
    const full = new Set<Role>(['admin', 'operator', 'approver', 'auditor'])
    const result = rolesWhenUnchecked('auditor', full)
    expect(result).toEqual(new Set(['admin', 'operator', 'approver']))
  })

  it('unchecking a role not present is a no-op', () => {
    const current = new Set<Role>(['admin'])
    const result = rolesWhenUnchecked('auditor', current)
    expect(result).toEqual(new Set(['admin']))
  })

  it('does not mutate the original set', () => {
    const original = new Set<Role>(['admin', 'operator', 'approver', 'auditor'])
    rolesWhenUnchecked('admin', original)
    expect(original.size).toBe(4)
  })
})

describe('highestSelectedRole', () => {
  it('returns null when the set is empty', () => {
    expect(highestSelectedRole(new Set<Role>())).toBeNull()
  })

  it('returns admin when admin is the only selected role', () => {
    expect(highestSelectedRole(new Set<Role>(['admin']))).toBe('admin')
  })

  it('returns admin when all roles are selected', () => {
    const full = new Set<Role>(['admin', 'operator', 'approver', 'auditor'])
    expect(highestSelectedRole(full)).toBe('admin')
  })

  it('returns operator when operator is the highest selected', () => {
    const roles = new Set<Role>(['operator', 'approver', 'auditor'])
    expect(highestSelectedRole(roles)).toBe('operator')
  })

  it('returns approver when only approver and auditor are selected', () => {
    expect(highestSelectedRole(new Set<Role>(['approver', 'auditor']))).toBe('approver')
  })

  it('returns auditor when only auditor is selected', () => {
    expect(highestSelectedRole(new Set<Role>(['auditor']))).toBe('auditor')
  })
})
