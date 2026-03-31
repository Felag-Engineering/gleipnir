import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import type { ApiRunStep } from '@/api/types'
import { parseStep } from './types'
import { ThoughtBlock } from './ThoughtBlock'

const meta: Meta<typeof ThoughtBlock> = {
  title: 'RunDetail/ThoughtBlock',
  component: ThoughtBlock,
}

export default meta
type Story = StoryObj<typeof ThoughtBlock>

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

export const ShortThought: Story = {
  args: {
    step: parseStep(makeRaw({
      content: JSON.stringify({ text: 'The user wants me to check the application logs for recent errors.' }),
    })) as ReturnType<typeof parseStep> & { type: 'thought' },
  },
}

export const LongThought: Story = {
  args: {
    step: parseStep(makeRaw({
      content: JSON.stringify({
        text: 'I need to analyze the deployment configuration. The current setup uses a blue-green deployment strategy with two identical production environments. Before making any changes, I should verify the health of both environments and check the rollback procedure. This will ensure we can recover quickly if the new deployment introduces regressions.',
      }),
    })) as ReturnType<typeof parseStep> & { type: 'thought' },
  },
}

export const MultiParagraph: Story = {
  args: {
    step: parseStep(makeRaw({
      content: JSON.stringify({
        text: 'First, I will check the current state of the database migrations to understand what schema version we are on.\n\nNext, I need to verify that the migration files are compatible with the target version. There may be breaking changes in columns that the application depends on.\n\nFinally, I should look at whether any indexes need to be rebuilt after the migration completes, as missing indexes can cause significant performance regressions under load.',
      }),
    })) as ReturnType<typeof parseStep> & { type: 'thought' },
  },
}
