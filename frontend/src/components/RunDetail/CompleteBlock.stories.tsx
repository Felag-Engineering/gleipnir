import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import type { ApiRunStep } from '@/api/types'
import { parseStep } from './types'
import { CompleteBlock } from './CompleteBlock'

const meta: Meta<typeof CompleteBlock> = {
  title: 'RunDetail/CompleteBlock',
  component: CompleteBlock,
}

export default meta
type Story = StoryObj<typeof CompleteBlock>

function makeRaw(overrides: Partial<ApiRunStep> = {}): ApiRunStep {
  return {
    id: 'step-1',
    run_id: 'run-1',
    step_number: 0,
    type: 'complete',
    content: '{}',
    token_cost: 0,
    created_at: '2026-03-10T12:00:00Z',
    ...overrides,
  }
}

export const WithDuration: Story = {
  args: {
    step: parseStep(makeRaw({
      content: JSON.stringify({ message: 'run completed successfully' }),
    })) as ReturnType<typeof parseStep> & { type: 'complete' },
    durationSeconds: 45,
  },
}

export const LongDuration: Story = {
  args: {
    step: parseStep(makeRaw({
      content: JSON.stringify({ message: 'run completed successfully' }),
    })) as ReturnType<typeof parseStep> & { type: 'complete' },
    durationSeconds: 3723,
  },
}

export const NoDuration: Story = {
  args: {
    step: parseStep(makeRaw({
      content: JSON.stringify({ message: 'run completed successfully' }),
    })) as ReturnType<typeof parseStep> & { type: 'complete' },
    durationSeconds: null,
  },
}
