import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { ToolAccordionRow } from './ToolAccordionRow'
import type { ApiMcpTool } from '@/api/types'

const echoTool: ApiMcpTool = {
  id: 't1',
  server_id: 'srv-1',
  name: 'echo',
  description: 'Echo the provided message back unchanged.',
  input_schema: {
    properties: {
      message: { title: 'Message', type: 'string' },
    },
    required: ['message'],
    type: 'object',
  },
}

const noParamTool: ApiMcpTool = {
  id: 't2',
  server_id: 'srv-1',
  name: 'get_current_time',
  description: 'Return the current UTC time.',
  input_schema: { properties: {}, type: 'object' },
}

const multiParamTool: ApiMcpTool = {
  id: 't3',
  server_id: 'srv-1',
  name: 'send_notification',
  description: 'Send a notification.',
  input_schema: {
    properties: {
      channel: { title: 'Channel', type: 'string' },
      message: { title: 'Message', type: 'string' },
    },
    required: ['channel', 'message'],
    type: 'object',
  },
}

const emptySchema: ApiMcpTool = {
  id: 't4',
  server_id: 'srv-1',
  name: 'broken_tool',
  description: 'Has no schema.',
  input_schema: {},
}

const noop = () => {}

describe('ToolAccordionRow', () => {
  it('shows tool name in collapsed state', () => {
    render(<ToolAccordionRow tool={echoTool} expanded={false} onToggle={noop} />)
    expect(screen.getByText('echo')).toBeInTheDocument()
  })

  it('shows param count hint when collapsed', () => {
    render(<ToolAccordionRow tool={echoTool} expanded={false} onToggle={noop} />)
    expect(screen.getByText('1 param')).toBeInTheDocument()
  })

  it('shows "no params" for tools without parameters', () => {
    render(<ToolAccordionRow tool={noParamTool} expanded={false} onToggle={noop} />)
    expect(screen.getByText('no params')).toBeInTheDocument()
  })

  it('shows "2 params" for multi-param tools', () => {
    render(<ToolAccordionRow tool={multiParamTool} expanded={false} onToggle={noop} />)
    expect(screen.getByText('2 params')).toBeInTheDocument()
  })

  it('does not show description when collapsed', () => {
    render(<ToolAccordionRow tool={echoTool} expanded={false} onToggle={noop} />)
    expect(screen.queryByText('Echo the provided message back unchanged.')).not.toBeInTheDocument()
  })

  it('shows description and parameters when expanded', () => {
    render(<ToolAccordionRow tool={echoTool} expanded={true} onToggle={noop} />)
    expect(screen.getByText('Echo the provided message back unchanged.')).toBeInTheDocument()
    expect(screen.getByText('message')).toBeInTheDocument()
    expect(screen.getByText('string')).toBeInTheDocument()
    expect(screen.getByText('required')).toBeInTheDocument()
  })

  it('shows "No parameters" when expanded with no params', () => {
    render(<ToolAccordionRow tool={noParamTool} expanded={true} onToggle={noop} />)
    expect(screen.getByText('No parameters')).toBeInTheDocument()
  })

  it('handles empty input_schema gracefully', () => {
    render(<ToolAccordionRow tool={emptySchema} expanded={false} onToggle={noop} />)
    expect(screen.getByText('no params')).toBeInTheDocument()
  })

  it('sets aria-expanded correctly', () => {
    render(<ToolAccordionRow tool={echoTool} expanded={false} onToggle={noop} />)
    expect(screen.getByRole('button')).toHaveAttribute('aria-expanded', 'false')
  })

  it('calls onToggle when clicked', () => {
    const onToggle = vi.fn()
    render(<ToolAccordionRow tool={echoTool} expanded={false} onToggle={onToggle} />)
    fireEvent.click(screen.getByRole('button'))
    expect(onToggle).toHaveBeenCalledOnce()
  })
})
