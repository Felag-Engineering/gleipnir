import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import type { ApiRunStep } from '@/api/types'
import { parseStep } from './types'
import { ThinkingBlock } from './ThinkingBlock'

const meta: Meta<typeof ThinkingBlock> = {
  title: 'RunDetail/ThinkingBlock',
  component: ThinkingBlock,
}

export default meta
type Story = StoryObj<typeof ThinkingBlock>

function makeRaw(overrides: Partial<ApiRunStep> = {}): ApiRunStep {
  return {
    id: 'step-1',
    run_id: 'run-1',
    step_number: 0,
    type: 'thinking',
    content: '{}',
    token_cost: 0,
    created_at: '2026-03-10T12:00:00Z',
    ...overrides,
  }
}

export const Collapsed: Story = {
  args: {
    step: parseStep(makeRaw({
      content: JSON.stringify({ text: 'The user wants me to check the application logs for recent errors.', redacted: false }),
      token_cost: 150,
    })) as ReturnType<typeof parseStep> & { type: 'thinking' },
    defaultExpanded: false,
  },
}

export const Expanded: Story = {
  args: {
    step: parseStep(makeRaw({
      content: JSON.stringify({ text: 'The user wants me to check the application logs for recent errors.', redacted: false }),
      token_cost: 150,
    })) as ReturnType<typeof parseStep> & { type: 'thinking' },
    defaultExpanded: true,
  },
}

export const Redacted: Story = {
  args: {
    step: parseStep(makeRaw({
      content: JSON.stringify({ text: '', redacted: true }),
      token_cost: 200,
    })) as ReturnType<typeof parseStep> & { type: 'thinking' },
    defaultExpanded: true,
  },
}

export const LongContent: Story = {
  args: {
    step: parseStep(makeRaw({
      content: JSON.stringify({
        text: 'I need to analyze the deployment configuration carefully before proceeding.\n\nThe current setup uses a blue-green deployment strategy with two identical production environments. Before making any changes, I should verify the health of both environments and check the rollback procedure.\n\nAdditionally, I need to consider the database migration state. If the schema has changed, I must ensure backward compatibility so that the old environment can still read records written by the new code during the transition window.\n\nFinally, I should check whether any feature flags are in play. If the new code path is gated behind a flag, I can deploy first and then flip the flag — this gives us a safe rollback option that doesn\'t require a full redeploy.',
        redacted: false,
      }),
      token_cost: 3400,
    })) as ReturnType<typeof parseStep> & { type: 'thinking' },
    defaultExpanded: true,
  },
}
