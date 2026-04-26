import { describe, it, expect } from 'vitest'
import { splitToolkit, groupToolsByToolkit } from './arcade'
import type { ApiMcpTool } from '@/api/types'

function makeTool(id: string, name: string): ApiMcpTool {
  return {
    id,
    server_id: 'srv-1',
    name,
    description: '',
    input_schema: {},
    enabled: true,
  }
}

describe('splitToolkit', () => {
  it('splits on first dot', () => {
    expect(splitToolkit('Gmail.SendEmail')).toEqual({ toolkit: 'Gmail', action: 'SendEmail' })
  })

  it('returns empty toolkit for name with no dot', () => {
    expect(splitToolkit('SendEmail')).toEqual({ toolkit: '', action: 'SendEmail' })
  })

  it('only splits on first dot for multi-dot names', () => {
    expect(splitToolkit('Gmail.Send.Email')).toEqual({ toolkit: 'Gmail', action: 'Send.Email' })
  })

  it('returns empty strings for empty input', () => {
    expect(splitToolkit('')).toEqual({ toolkit: '', action: '' })
  })
})

describe('groupToolsByToolkit', () => {
  it('groups tools by toolkit prefix', () => {
    const tools = [
      makeTool('1', 'Gmail.SendEmail'),
      makeTool('2', 'Gmail.ListEmails'),
      makeTool('3', 'GoogleCalendar.CreateEvent'),
    ]

    const groups = groupToolsByToolkit(tools)

    expect(groups.size).toBe(2)
    expect(groups.get('Gmail')).toHaveLength(2)
    expect(groups.get('GoogleCalendar')).toHaveLength(1)
  })

  it('preserves insertion order within each toolkit', () => {
    const tools = [
      makeTool('1', 'Gmail.SendEmail'),
      makeTool('2', 'Gmail.ListEmails'),
      makeTool('3', 'Gmail.Archive'),
    ]

    const groups = groupToolsByToolkit(tools)
    const gmailTools = groups.get('Gmail')!
    expect(gmailTools[0].name).toBe('Gmail.SendEmail')
    expect(gmailTools[1].name).toBe('Gmail.ListEmails')
    expect(gmailTools[2].name).toBe('Gmail.Archive')
  })

  it('skips tools without a toolkit prefix (no dot)', () => {
    const tools = [
      makeTool('1', 'Gmail.SendEmail'),
      makeTool('2', 'nodottool'),
    ]

    const groups = groupToolsByToolkit(tools)
    expect(groups.size).toBe(1)
    expect(groups.has('')).toBe(false)
  })

  it('returns empty map for empty input', () => {
    expect(groupToolsByToolkit([])).toEqual(new Map())
  })
})
