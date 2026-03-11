import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import type { ApiRunStep } from '@/api/types'
import { parseStep } from './types'
import type { GrantedToolEntry } from './types'
import { StepCard } from './StepCard'

const meta: Meta<typeof StepCard> = {
  title: 'RunDetail/StepCard',
  component: StepCard,
}

export default meta
type Story = StoryObj<typeof StepCard>

function makeRaw(overrides: Partial<ApiRunStep> = {}): ApiRunStep {
  return {
    id: 'step-1',
    run_id: 'run-1',
    step_number: 1,
    type: 'thought',
    content: '{}',
    token_cost: 42,
    created_at: '2026-03-10T12:00:00Z',
    ...overrides,
  }
}

const emptyRoleMap = new Map<string, GrantedToolEntry['Role']>()
const roleMap = new Map<string, GrantedToolEntry['Role']>([
  ['read_file', 'sensor'],
  ['write_file', 'actuator'],
  ['send_slack', 'actuator'],
  ['list_files', 'sensor'],
])

export const Thought: Story = {
  args: {
    step: parseStep(makeRaw({
      type: 'thought',
      content: JSON.stringify({ text: 'I should start by reading the log file to understand what happened during the last deployment window.' }),
    })),
    toolRoleMap: emptyRoleMap,
  },
}

export const ToolCallSensor: Story = {
  name: 'Tool call — sensor (blue)',
  args: {
    step: parseStep(makeRaw({
      type: 'tool_call',
      content: JSON.stringify({ tool_name: 'read_file', server_id: 'fs-server', input: { path: '/var/log/app.log', lines: 50 } }),
    })),
    toolRoleMap: roleMap,
  },
}

export const ToolCallActuator: Story = {
  name: 'Tool call — actuator (orange)',
  args: {
    step: parseStep(makeRaw({
      type: 'tool_call',
      content: JSON.stringify({ tool_name: 'write_file', server_id: 'fs-server', input: { path: '/tmp/report.txt', content: 'All checks passed.' } }),
    })),
    toolRoleMap: roleMap,
  },
}

export const ToolResultOk: Story = {
  name: 'Tool result — success',
  args: {
    step: parseStep(makeRaw({
      type: 'tool_result',
      content: JSON.stringify({ tool_name: 'read_file', output: JSON.stringify({ lines: ['INFO app started', 'INFO ready'] }), is_error: false }),
    })),
    toolRoleMap: emptyRoleMap,
  },
}

export const ToolResultError: Story = {
  name: 'Tool result — error',
  args: {
    step: parseStep(makeRaw({
      type: 'tool_result',
      content: JSON.stringify({ tool_name: 'write_file', output: 'permission denied: /tmp/report.txt', is_error: true }),
    })),
    toolRoleMap: emptyRoleMap,
  },
}

export const Error: Story = {
  args: {
    step: parseStep(makeRaw({
      type: 'error',
      content: JSON.stringify({ message: 'MCP server unreachable after 3 retries', code: 'MCP_TIMEOUT' }),
    })),
    toolRoleMap: emptyRoleMap,
  },
}

export const Complete: Story = {
  args: {
    step: parseStep(makeRaw({
      type: 'complete',
      content: JSON.stringify({ message: 'Backup verification complete. All 12 shards healthy.' }),
    })),
    toolRoleMap: emptyRoleMap,
  },
}

export const ApprovalRequest: Story = {
  args: {
    step: parseStep(makeRaw({
      type: 'approval_request',
      content: JSON.stringify({ tool: 'send_slack', input: { channel: '#incidents', message: 'Deploying hotfix to prod.' } }),
    })),
    toolRoleMap: emptyRoleMap,
  },
}

export const AllTypes: Story = {
  render: () => (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <StepCard
        step={parseStep(makeRaw({ type: 'thought', content: JSON.stringify({ text: 'Let me check the logs.' }) }))}
        toolRoleMap={emptyRoleMap}
      />
      <StepCard
        step={parseStep(makeRaw({ type: 'tool_call', content: JSON.stringify({ tool_name: 'read_file', server_id: 'fs', input: { path: '/var/log/app.log' } }) }))}
        toolRoleMap={roleMap}
      />
      <StepCard
        step={parseStep(makeRaw({ type: 'tool_call', content: JSON.stringify({ tool_name: 'write_file', server_id: 'fs', input: { path: '/tmp/out.txt', content: 'done' } }) }))}
        toolRoleMap={roleMap}
      />
      <StepCard
        step={parseStep(makeRaw({ type: 'tool_result', content: JSON.stringify({ tool_name: 'read_file', output: '["INFO ready"]', is_error: false }) }))}
        toolRoleMap={emptyRoleMap}
      />
      <StepCard
        step={parseStep(makeRaw({ type: 'error', content: JSON.stringify({ message: 'timeout', code: 'TIMEOUT' }) }))}
        toolRoleMap={emptyRoleMap}
      />
      <StepCard
        step={parseStep(makeRaw({ type: 'complete', content: JSON.stringify({ message: 'Done.' }) }))}
        toolRoleMap={emptyRoleMap}
      />
    </div>
  ),
}
