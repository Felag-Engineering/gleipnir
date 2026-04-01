import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import type { ApiRunStep } from '@/api/types'
import { parseStep } from './types'
import { ErrorBlock } from './ErrorBlock'

const meta: Meta<typeof ErrorBlock> = {
  title: 'RunDetail/ErrorBlock',
  component: ErrorBlock,
}

export default meta
type Story = StoryObj<typeof ErrorBlock>

function makeRaw(overrides: Partial<ApiRunStep> = {}): ApiRunStep {
  return {
    id: 'step-1',
    run_id: 'run-1',
    step_number: 0,
    type: 'error',
    content: '{}',
    token_cost: 0,
    created_at: '2026-03-10T12:00:00Z',
    ...overrides,
  }
}

export const ShortError: Story = {
  args: {
    step: parseStep(makeRaw({
      content: JSON.stringify({
        message: 'upstream MCP server returned HTTP 503',
        code: 'api_error',
      }),
    })) as ReturnType<typeof parseStep> & { type: 'error' },
  },
}

export const LongError: Story = {
  args: {
    step: parseStep(makeRaw({
      content: JSON.stringify({
        message: 'Error: context deadline exceeded\n    at runAgent (agent/runner.go:142)\n    at BoundAgent.Run (agent/bound.go:87)\n    at triggerWebhook (trigger/webhook.go:63)\nCaused by: dial tcp 10.0.0.5:8080: i/o timeout\n    upstream server did not respond within the 30s tool execution timeout',
        code: 'internal_error',
      }),
    })) as ReturnType<typeof parseStep> & { type: 'error' },
  },
}

export const NoCode: Story = {
  args: {
    step: parseStep(makeRaw({
      content: JSON.stringify({
        message: 'the agent run was cancelled before completion',
        code: '',
      }),
    })) as ReturnType<typeof parseStep> & { type: 'error' },
  },
}
