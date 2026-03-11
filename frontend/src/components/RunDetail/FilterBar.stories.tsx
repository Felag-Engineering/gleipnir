import { useState } from 'react'
import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { FilterBar } from './FilterBar'
import type { FilterKey } from './FilterBar'

const meta: Meta<typeof FilterBar> = {
  title: 'RunDetail/FilterBar',
  component: FilterBar,
}

export default meta
type Story = StoryObj<typeof FilterBar>

const COUNTS: Record<FilterKey, number> = {
  all: 24,
  thought: 8,
  tool_call: 7,
  tool_result: 7,
  error: 2,
}

export const AllActive: Story = {
  args: {
    active: 'all',
    counts: COUNTS,
    onChange: () => {},
  },
}

export const ThoughtsActive: Story = {
  args: {
    active: 'thought',
    counts: COUNTS,
    onChange: () => {},
  },
}

export const ErrorsActive: Story = {
  args: {
    active: 'error',
    counts: COUNTS,
    onChange: () => {},
  },
}

export const NoErrors: Story = {
  args: {
    active: 'all',
    counts: { ...COUNTS, error: 0 },
    onChange: () => {},
  },
}

export const Interactive: Story = {
  render: () => {
    // eslint-disable-next-line react-hooks/rules-of-hooks
    const [active, setActive] = useState<FilterKey>('all')
    return <FilterBar active={active} counts={COUNTS} onChange={setActive} />
  },
}
