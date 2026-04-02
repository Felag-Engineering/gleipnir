import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/tokens.css'
import type { ApiRunStep } from '@/api/types'
import { queryKeys } from '@/hooks/queryKeys'
import { parseStep } from './types'
import type { ToolBlockData } from './types'
import { ToolBlock } from './ToolBlock'

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: false } },
})

function makeApproverQueryClient() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  qc.setQueryData(queryKeys.currentUser.all, {
    id: 'user-1',
    username: 'admin',
    roles: ['admin'],
  })
  return qc
}

const meta: Meta<typeof ToolBlock> = {
  title: 'RunDetail/ToolBlock',
  component: ToolBlock,
  decorators: [
    (Story) => (
      <QueryClientProvider client={queryClient}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof ToolBlock>

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

export const Success: Story = {
  args: {
    block: {
      approval: null,
      call: parseStep(makeRaw({
        id: 'step-call',
        type: 'tool_call',
        created_at: '2026-03-10T12:00:00Z',
        content: JSON.stringify({
          tool_name: 'read_file',
          server_id: 'fs-server',
          input: { path: '/var/log/app.log', lines: 50 },
        }),
      })) as ToolBlockData['call'],
      result: parseStep(makeRaw({
        id: 'step-result',
        type: 'tool_result',
        created_at: '2026-03-10T12:00:02Z',
        content: JSON.stringify({
          tool_name: 'read_file',
          output: JSON.stringify({ lines: ['INFO app started', 'INFO ready'] }),
          is_error: false,
        }),
      })) as ToolBlockData['result'],
    } satisfies ToolBlockData,
    runId: 'run-1',
    runStatus: 'complete',
  },
}

export const ErrorResult: Story = {
  name: 'Error result',
  args: {
    block: {
      approval: null,
      call: parseStep(makeRaw({
        id: 'step-call',
        type: 'tool_call',
        content: JSON.stringify({
          tool_name: 'write_file',
          server_id: 'fs-server',
          input: { path: '/tmp/report.txt', content: 'All checks passed.' },
        }),
      })) as ToolBlockData['call'],
      result: parseStep(makeRaw({
        id: 'step-result',
        type: 'tool_result',
        content: JSON.stringify({
          tool_name: 'write_file',
          output: 'permission denied: /tmp/report.txt',
          is_error: true,
        }),
      })) as ToolBlockData['result'],
    } satisfies ToolBlockData,
    runId: 'run-1',
    runStatus: 'failed',
  },
}

export const NoResult: Story = {
  name: 'No result (pending/interrupted)',
  args: {
    block: {
      approval: null,
      call: parseStep(makeRaw({
        id: 'step-call',
        type: 'tool_call',
        content: JSON.stringify({
          tool_name: 'list_files',
          server_id: 'fs-server',
          input: { directory: '/var/data' },
        }),
      })) as ToolBlockData['call'],
      result: null,
    } satisfies ToolBlockData,
    runId: 'run-1',
    runStatus: 'running',
  },
}

export const ApprovalPending: Story = {
  name: 'Approval pending',
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeApproverQueryClient()}>
        <Story />
      </QueryClientProvider>
    ),
  ],
  args: {
    block: {
      approval: parseStep(makeRaw({
        id: 'step-approval',
        type: 'approval_request',
        content: JSON.stringify({
          tool: 'send_slack',
          input: { channel: '#incidents', message: 'Deploying hotfix to prod.' },
        }),
      })) as ToolBlockData['approval'],
      call: parseStep(makeRaw({
        id: 'step-call',
        type: 'tool_call',
        content: JSON.stringify({
          tool_name: 'send_slack',
          server_id: 'slack-server',
          input: { channel: '#incidents', message: 'Deploying hotfix to prod.' },
        }),
      })) as ToolBlockData['call'],
      result: null,
    } satisfies ToolBlockData,
    runId: 'run-1',
    runStatus: 'waiting_for_approval',
  },
}

export const ApprovalResolved: Story = {
  name: 'Approval resolved (granted)',
  args: {
    block: {
      approval: parseStep(makeRaw({
        id: 'step-approval',
        type: 'approval_request',
        content: JSON.stringify({
          tool: 'send_slack',
          input: { channel: '#incidents', message: 'Deploying hotfix to prod.' },
        }),
      })) as ToolBlockData['approval'],
      call: parseStep(makeRaw({
        id: 'step-call',
        type: 'tool_call',
        created_at: '2026-03-10T12:00:00Z',
        content: JSON.stringify({
          tool_name: 'send_slack',
          server_id: 'slack-server',
          input: { channel: '#incidents', message: 'Deploying hotfix to prod.' },
        }),
      })) as ToolBlockData['call'],
      result: parseStep(makeRaw({
        id: 'step-result',
        type: 'tool_result',
        created_at: '2026-03-10T12:00:01Z',
        content: JSON.stringify({
          tool_name: 'send_slack',
          output: '"Message sent"',
          is_error: false,
        }),
      })) as ToolBlockData['result'],
    } satisfies ToolBlockData,
    runId: 'run-1',
    runStatus: 'complete',
  },
}

export const ApprovalDenied: Story = {
  name: 'Approval denied/timed out',
  args: {
    block: {
      approval: parseStep(makeRaw({
        id: 'step-approval',
        type: 'approval_request',
        content: JSON.stringify({
          tool: 'send_slack',
          input: { channel: '#incidents', message: 'Deploying hotfix to prod.' },
        }),
      })) as ToolBlockData['approval'],
      call: null,
      result: null,
    } satisfies ToolBlockData,
    runId: 'run-1',
    runStatus: 'failed',
  },
}
