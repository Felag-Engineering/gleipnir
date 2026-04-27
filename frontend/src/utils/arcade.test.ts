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
  it('splits on first underscore', () => {
    expect(splitToolkit('Gmail_SendEmail')).toEqual({ toolkit: 'Gmail', action: 'SendEmail' })
  })

  it('returns empty toolkit for name with no underscore', () => {
    expect(splitToolkit('SendEmail')).toEqual({ toolkit: '', action: 'SendEmail' })
  })

  it('only splits on first underscore for multi-underscore names', () => {
    expect(splitToolkit('Gmail_Send_Email')).toEqual({ toolkit: 'Gmail', action: 'Send_Email' })
  })

  it('returns empty strings for empty input', () => {
    expect(splitToolkit('')).toEqual({ toolkit: '', action: '' })
  })
})

describe('groupToolsByToolkit', () => {
  it('groups tools by toolkit prefix', () => {
    const tools = [
      makeTool('1', 'Gmail_SendEmail'),
      makeTool('2', 'Gmail_ListEmails'),
      makeTool('3', 'GoogleCalendar_CreateEvent'),
    ]

    const groups = groupToolsByToolkit(tools)

    expect(groups.size).toBe(2)
    expect(groups.get('Gmail')).toHaveLength(2)
    expect(groups.get('GoogleCalendar')).toHaveLength(1)
  })

  it('preserves insertion order within each toolkit', () => {
    const tools = [
      makeTool('1', 'Gmail_SendEmail'),
      makeTool('2', 'Gmail_ListEmails'),
      makeTool('3', 'Gmail_Archive'),
    ]

    const groups = groupToolsByToolkit(tools)
    const gmailTools = groups.get('Gmail')!
    expect(gmailTools[0].name).toBe('Gmail_SendEmail')
    expect(gmailTools[1].name).toBe('Gmail_ListEmails')
    expect(gmailTools[2].name).toBe('Gmail_Archive')
  })

  it('skips tools without a toolkit prefix (no underscore)', () => {
    const tools = [
      makeTool('1', 'Gmail_SendEmail'),
      makeTool('2', 'nounderscoretool'),
    ]

    const groups = groupToolsByToolkit(tools)
    expect(groups.size).toBe(1)
    expect(groups.has('')).toBe(false)
  })

  it('returns empty map for empty input', () => {
    expect(groupToolsByToolkit([])).toEqual(new Map())
  })
})
