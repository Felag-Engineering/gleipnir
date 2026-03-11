import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import type { ApiRunStep } from '@/api/types'
import { parseStep } from './types'
import type { GrantedToolEntry } from './types'
import { StepTimeline } from './StepTimeline'

const meta: Meta<typeof StepTimeline> = {
  title: 'RunDetail/StepTimeline',
  component: StepTimeline,
}

export default meta
type Story = StoryObj<typeof StepTimeline>

function makeRaw(overrides: Partial<ApiRunStep> = {}): ApiRunStep {
  return {
    id: 'step-1',
    run_id: 'run-1',
    step_number: 1,
    type: 'thought',
    content: '{}',
    token_cost: 0,
    created_at: '2026-03-10T12:00:00Z',
    ...overrides,
  }
}

const roleMap = new Map<string, GrantedToolEntry['Role']>([
  ['read_file', 'sensor'],
  ['write_file', 'actuator'],
])

const FULL_STEPS = [
  parseStep(makeRaw({ id: 's1', step_number: 1, type: 'capability_snapshot', content: JSON.stringify([
    { ServerName: 'fs-server', ToolName: 'read_file', Role: 'sensor', Approval: 'none', Timeout: 30, OnTimeout: 'fail' },
    { ServerName: 'fs-server', ToolName: 'write_file', Role: 'actuator', Approval: 'required', Timeout: 60, OnTimeout: 'fail' },
  ]) })),
  parseStep(makeRaw({ id: 's2', step_number: 2, type: 'thought', content: JSON.stringify({ text: 'I should start by reading the log file.' }) })),
  parseStep(makeRaw({ id: 's3', step_number: 3, type: 'tool_call', content: JSON.stringify({ tool_name: 'read_file', server_id: 'fs-server', input: { path: '/var/log/app.log', lines: 100 } }) })),
  parseStep(makeRaw({ id: 's4', step_number: 4, type: 'tool_result', content: JSON.stringify({ tool_name: 'read_file', output: JSON.stringify({ lines: ['INFO app started', 'INFO ready', 'WARN slow query'] }), is_error: false }) })),
  parseStep(makeRaw({ id: 's5', step_number: 5, type: 'thought', content: JSON.stringify({ text: 'There is a slow query warning. I should write a report.' }) })),
  parseStep(makeRaw({ id: 's6', step_number: 6, type: 'tool_call', content: JSON.stringify({ tool_name: 'write_file', server_id: 'fs-server', input: { path: '/tmp/report.txt', content: 'Slow query detected at startup.' } }) })),
  parseStep(makeRaw({ id: 's7', step_number: 7, type: 'tool_result', content: JSON.stringify({ tool_name: 'write_file', output: 'ok', is_error: false }) })),
  parseStep(makeRaw({ id: 's8', step_number: 8, type: 'complete', content: JSON.stringify({ message: 'Report written to /tmp/report.txt.' }) })),
]

export const FullRun: Story = {
  args: { steps: FULL_STEPS, toolRoleMap: roleMap },
}

export const WithError: Story = {
  args: {
    steps: [
      ...FULL_STEPS.slice(0, 4),
      parseStep(makeRaw({ id: 's5e', step_number: 5, type: 'error', content: JSON.stringify({ message: 'write_file: permission denied', code: 'FS_PERMISSION' }) })),
    ],
    toolRoleMap: roleMap,
  },
}

export const Empty: Story = {
  args: { steps: [], toolRoleMap: roleMap },
}

export const OnlyCapabilitySnapshot: Story = {
  args: {
    steps: [FULL_STEPS[0]],
    toolRoleMap: roleMap,
  },
}
