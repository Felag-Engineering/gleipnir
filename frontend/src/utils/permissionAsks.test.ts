import { describe, it, expect } from 'vitest'
import type { ApiAttentionItem } from '@/api/types'
import { permissionAskRunIds } from './permissionAsks'
import { runStatusLabel } from '@/components/dashboard/types'

function item(overrides: Partial<ApiAttentionItem>): ApiAttentionItem {
  return {
    type: 'tool_input',
    request_id: 'req',
    run_id: 'run',
    policy_id: 'p',
    policy_name: 'policy',
    tool_name: 'relay.run_operation',
    message: '',
    expires_at: null,
    created_at: '2026-01-01T00:00:00Z',
    ...overrides,
  } as ApiAttentionItem
}

describe('permissionAskRunIds', () => {
  it('collects only tool_input rows whose kind is permission', () => {
    const ids = permissionAskRunIds([
      item({ run_id: 'perm', elicitation_kind: 'permission' }),
      item({ run_id: 'info', elicitation_kind: 'information' }),
      item({ run_id: 'native', type: 'feedback', elicitation_kind: '' }),
      item({ run_id: 'gate', type: 'approval', elicitation_kind: '' }),
    ])
    expect([...ids]).toEqual(['perm'])
  })

  it('is empty for an empty queue', () => {
    expect(permissionAskRunIds([]).size).toBe(0)
  })
})

describe('runStatusLabel', () => {
  it.each([
    ['waiting_for_feedback', true, 'Awaiting Approval'],
    ['waiting_for_feedback', false, 'Awaiting Feedback'],
    ['waiting_for_approval', false, 'Awaiting Approval'],
    ['running', true, 'Running'],
    ['complete', true, 'Complete'],
  ] as const)('%s (permission ask: %s) → %s', (status, awaitingPermission, label) => {
    expect(runStatusLabel(status, awaitingPermission)).toBe(label)
  })
})
