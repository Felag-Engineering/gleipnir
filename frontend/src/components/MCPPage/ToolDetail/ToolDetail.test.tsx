import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { ToolDetail } from './ToolDetail'
import type { ApiMcpTool } from '@/api/types'

const tool: ApiMcpTool = {
  id: 't1',
  server_id: 'srv-1',
  name: 'run_command',
  description: 'Run a command.\nSecond line.',
  input_schema: {
    type: 'object',
    required: ['command'],
    properties: {
      command: { type: 'string', description: 'The command line.', maxLength: 100 },
      shell: { type: 'string', enum: ['sh', 'bash'], default: 'sh' },
      options: { type: 'object', properties: { dir: { type: 'string' } } },
    },
  },
  enabled: true,
}

describe('ToolDetail', () => {
  it('shows the name, status, policy reference and description', () => {
    render(<ToolDetail tool={tool} serverName="fleet" />)

    expect(screen.getByRole('heading', { name: 'run_command' })).toBeInTheDocument()
    expect(screen.getByText('Enabled')).toBeInTheDocument()
    expect(screen.getByText('fleet.run_command')).toBeInTheDocument()
    expect(screen.getByText(/Run a command\.\s+Second line\./)).toBeInTheDocument()
  })

  it('says nothing about argument checking when enforcement is exact', () => {
    render(<ToolDetail tool={{ ...tool, arg_enforcement: 'exact' }} serverName="fleet" />)
    expect(screen.queryByText(/Argument checking/)).not.toBeInTheDocument()
  })

  it.each([
    ['no_canonical_schema', "the tool's schema could not be processed"],
    ['schema_uncompilable', "the tool's schema could not be used for validation"],
    ['no_schema', 'the tool does not declare an argument schema'],
  ] as const)('explains reduced argument checking for %s', (state, reason) => {
    render(<ToolDetail tool={{ ...tool, arg_enforcement: state }} serverName="fleet" />)
    expect(screen.getByText(`Argument checking: reduced — ${reason}.`, { exact: false })).toBeInTheDocument()
    expect(screen.getByText('Reduced argument checking')).toBeInTheDocument()
  })

  it('shows each parameter with its type, requirement, description and facts', () => {
    render(<ToolDetail tool={tool} serverName="fleet" />)

    const items = screen.getAllByRole('listitem')
    expect(items.map((li) => li.querySelector('code')?.textContent)).toEqual(['command', 'shell', 'options', 'options.dir'])

    expect(items[0]).toHaveTextContent('string')
    expect(items[0]).toHaveTextContent('required')
    expect(items[0]).toHaveTextContent('The command line.')
    expect(items[0]).toHaveTextContent('max 100 chars')

    expect(items[1]).toHaveTextContent('optional')
    expect(items[1]).toHaveTextContent(/Allowed\s*shbash/)
    expect(items[1]).toHaveTextContent(/Default\s*sh/)
  })

  it('counts top-level parameters only', () => {
    render(<ToolDetail tool={tool} serverName="fleet" />)
    expect(screen.getByRole('heading', { name: /Parameters/ })).toHaveTextContent('Parameters3')
  })

  it('says so when the tool takes no parameters or has no description', () => {
    render(<ToolDetail tool={{ ...tool, description: '', input_schema: { type: 'object' } }} serverName="fleet" />)
    expect(screen.getByText('This tool takes no parameters.')).toBeInTheDocument()
    expect(screen.getByText('The server did not provide a description.')).toBeInTheDocument()
  })

  it('warns that the list may be incomplete for a combinator schema', () => {
    render(<ToolDetail tool={{ ...tool, input_schema: { oneOf: [{ type: 'object' }] } }} serverName="fleet" />)
    expect(screen.getByText(/may be\s+incomplete/)).toBeInTheDocument()
    expect(screen.queryByText('This tool takes no parameters.')).not.toBeInTheDocument()
  })

  it('lists the agents that grant the tool, or says none do', () => {
    const { rerender } = render(<ToolDetail tool={tool} serverName="fleet" usedBy={['nightly', 'triage']} />)
    expect(screen.getByText('nightly')).toBeInTheDocument()
    expect(screen.getByText('triage')).toBeInTheDocument()

    rerender(<ToolDetail tool={tool} serverName="fleet" usedBy={[]} />)
    expect(screen.getByText('No agent grants this tool.')).toBeInTheDocument()
  })

  it('renders server-supplied text as plain text, not markup', () => {
    render(<ToolDetail tool={{ ...tool, description: '<b>bold</b> **md**' }} serverName="fleet" />)
    expect(screen.getByText('<b>bold</b> **md**')).toBeInTheDocument()
    expect(document.querySelector('b')).toBeNull()
  })

  describe('enable/disable', () => {
    it('is hidden without manage rights', () => {
      render(<ToolDetail tool={tool} serverName="fleet" onSetEnabled={vi.fn()} />)
      expect(screen.queryByRole('button', { name: /tool$/ })).not.toBeInTheDocument()
    })

    it('disables an enabled tool', () => {
      const onSetEnabled = vi.fn()
      render(<ToolDetail tool={tool} serverName="fleet" canManage onSetEnabled={onSetEnabled} />)
      fireEvent.click(screen.getByRole('button', { name: 'Disable tool' }))
      expect(onSetEnabled).toHaveBeenCalledWith(false)
    })

    it('enables a disabled tool and explains what disabled means', () => {
      const onSetEnabled = vi.fn()
      render(<ToolDetail tool={{ ...tool, enabled: false }} serverName="fleet" canManage onSetEnabled={onSetEnabled} />)
      expect(screen.getByText('Disabled')).toBeInTheDocument()
      expect(screen.getByText(/never registered with an agent/)).toBeInTheDocument()
      fireEvent.click(screen.getByRole('button', { name: 'Enable tool' }))
      expect(onSetEnabled).toHaveBeenCalledWith(true)
    })

    it('shows progress and blocks repeat clicks while updating', () => {
      render(<ToolDetail tool={tool} serverName="fleet" canManage onSetEnabled={vi.fn()} isUpdatingEnabled />)
      expect(screen.getByRole('button', { name: 'Disabling...' })).toBeDisabled()
    })
  })
})
