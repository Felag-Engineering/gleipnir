import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/tokens.css'
import type { ApiRunStep } from '@/api/types'
import { parseStep, pairToolBlocks } from './types'
import { StepTimeline } from './StepTimeline'

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: false } },
})

const meta: Meta<typeof StepTimeline> = {
  title: 'RunDetail/StepTimeline',
  component: StepTimeline,
  decorators: [
    (Story) => (
      <QueryClientProvider client={queryClient}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof StepTimeline>

function makeRaw(overrides: Partial<ApiRunStep> = {}): ApiRunStep {
  return {
    id: 'step-1',
    run_id: 'run-1',
    step_number: 0,
    type: 'thought',
    content: '{}',
    token_cost: 0,
    created_at: '2026-03-10T12:00:00Z',
    ...overrides,
  }
}

const FULL_STEPS = [
  parseStep(makeRaw({ id: 's1', step_number: 0, type: 'capability_snapshot', content: JSON.stringify([
    { ServerName: 'fs-server', ToolName: 'read_file', Role: 'tool', Approval: 'none', Timeout: 30, OnTimeout: 'fail' },
    { ServerName: 'fs-server', ToolName: 'write_file', Role: 'tool', Approval: 'required', Timeout: 60, OnTimeout: 'fail' },
  ]) })),
  parseStep(makeRaw({ id: 's2', step_number: 1, type: 'thought', content: JSON.stringify({ text: 'I should start by reading the log file.' }) })),
  parseStep(makeRaw({ id: 's3', step_number: 2, type: 'tool_call', content: JSON.stringify({ tool_name: 'read_file', server_id: 'fs-server', input: { path: '/var/log/app.log', lines: 100 } }) })),
  parseStep(makeRaw({ id: 's4', step_number: 3, type: 'tool_result', content: JSON.stringify({ tool_name: 'read_file', output: JSON.stringify({ lines: ['INFO app started', 'INFO ready', 'WARN slow query'] }), is_error: false }) })),
  parseStep(makeRaw({ id: 's5', step_number: 4, type: 'thought', content: JSON.stringify({ text: 'There is a slow query warning. I should write a report.' }) })),
  parseStep(makeRaw({ id: 's6', step_number: 5, type: 'tool_call', content: JSON.stringify({ tool_name: 'write_file', server_id: 'fs-server', input: { path: '/tmp/report.txt', content: 'Slow query detected at startup.' } }) })),
  parseStep(makeRaw({ id: 's7', step_number: 6, type: 'tool_result', content: JSON.stringify({ tool_name: 'write_file', output: 'ok', is_error: false }) })),
  parseStep(makeRaw({ id: 's8', step_number: 7, type: 'complete', content: JSON.stringify({ message: 'Report written to /tmp/report.txt.' }) })),
]

const defaultRunProps = {
  runId: 'run-1',
  runStatus: 'complete',
}

export const FullRun: Story = {
  args: { items: pairToolBlocks(FULL_STEPS), durationSeconds: 62, ...defaultRunProps },
}

export const WithError: Story = {
  args: {
    items: pairToolBlocks([
      ...FULL_STEPS.slice(0, 4),
      parseStep(makeRaw({ id: 's5e', step_number: 4, type: 'error', content: JSON.stringify({ message: 'write_file: permission denied', code: 'FS_PERMISSION' }) })),
    ]),
    durationSeconds: undefined,
    ...defaultRunProps,
  },
}

export const Empty: Story = {
  args: { items: [], durationSeconds: undefined, ...defaultRunProps },
}

export const OnlyCapabilitySnapshot: Story = {
  args: {
    items: pairToolBlocks([FULL_STEPS[0]]),
    durationSeconds: undefined,
    ...defaultRunProps,
  },
}

// FullRunAllBlockTypes shows every possible block type in one timeline — the
// acceptance criterion for visual regression coverage across all step kinds.
export const FullRunAllBlockTypes: Story = {
  args: {
    items: pairToolBlocks([
      // Capability snapshot at the top (rendered by CapabilitySnapshotCard)
      parseStep(makeRaw({ id: 'a1', step_number: 0, type: 'capability_snapshot', content: JSON.stringify([
        { ServerName: 'fs-server', ToolName: 'read_file', Role: 'tool', Approval: 'none', Timeout: 30, OnTimeout: 'fail' },
        { ServerName: 'fs-server', ToolName: 'write_file', Role: 'tool', Approval: 'required', Timeout: 60, OnTimeout: 'fail' },
      ]) })),
      // Thinking block (extended thinking from Claude)
      parseStep(makeRaw({ id: 'a2', step_number: 1, type: 'thinking', content: JSON.stringify({ text: 'I need to analyze the situation carefully before acting.', redacted: false }) })),
      // Thought block (agent reasoning)
      parseStep(makeRaw({ id: 'a3', step_number: 2, type: 'thought', content: JSON.stringify({ text: 'I should read the log file first to understand the failure.' }) })),
      // Tool call + tool result (non-gated, paired into ToolBlock)
      parseStep(makeRaw({ id: 'a4', step_number: 3, type: 'tool_call', content: JSON.stringify({ tool_name: 'read_file', server_id: 'fs-server', input: { path: '/var/log/app.log' } }) })),
      parseStep(makeRaw({ id: 'a5', step_number: 4, type: 'tool_result', content: JSON.stringify({ tool_name: 'read_file', output: '["ERROR: disk full"]', is_error: false }) })),
      // Approval-gated tool call (approval_request + tool_call + tool_result, paired into ToolBlock)
      parseStep(makeRaw({ id: 'a6', step_number: 5, type: 'approval_request', content: JSON.stringify({ tool: 'write_file', input: { path: '/var/log/app.log', content: '' } }) })),
      parseStep(makeRaw({ id: 'a7', step_number: 6, type: 'tool_call', content: JSON.stringify({ tool_name: 'write_file', server_id: 'fs-server', input: { path: '/var/log/app.log', content: '' } }) })),
      parseStep(makeRaw({ id: 'a8', step_number: 7, type: 'tool_result', content: JSON.stringify({ tool_name: 'write_file', output: 'ok', is_error: false }) })),
      // Error block
      parseStep(makeRaw({ id: 'a9', step_number: 8, type: 'error', content: JSON.stringify({ message: 'MCP server unreachable after 3 retries', code: 'MCP_TIMEOUT' }) })),
      // Feedback request (shows the feedback UI)
      parseStep(makeRaw({ id: 'a10', step_number: 9, type: 'feedback_request', content: JSON.stringify({ feedback_id: 'fb-001', tool: 'gleipnir.ask_operator', message: 'Should I continue?\n\nThe disk cleanup removed 42 GB. Proceed with restart?' }) })),
      // Feedback response (operator replied)
      parseStep(makeRaw({ id: 'a11', step_number: 10, type: 'feedback_response', content: JSON.stringify({ feedback_id: 'fb-001', response: 'Yes, proceed with the restart.' }) })),
      // Complete block at the end
      parseStep(makeRaw({ id: 'a12', step_number: 11, type: 'complete', content: JSON.stringify({ message: 'Disk cleaned and service restarted successfully.' }) })),
    ]),
    durationSeconds: 137,
    runId: 'run-1',
    runStatus: 'complete',
  },
}
