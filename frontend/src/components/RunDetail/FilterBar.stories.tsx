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
  all: 42,
  tool: 12,
  thought: 8,
  thinking: 6,
  error: 3,
  approval: 2,
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

export const ThinkingActive: Story = {
  args: {
    active: 'thinking',
    counts: COUNTS,
    onChange: () => {},
  },
}

export const ApprovalsActive: Story = {
  args: {
    active: 'approval',
    counts: COUNTS,
    onChange: () => {},
  },
}

export const ToolsActive: Story = {
  args: {
    active: 'tool',
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

// All counts are zero — badges should be hidden for every chip.
export const AllZeroCounts: Story = {
  args: {
    active: 'all',
    counts: { all: 0, tool: 0, thought: 0, thinking: 0, error: 0, approval: 0 },
    onChange: () => {},
  },
}

// Active chip with non-zero count demonstrates the inverted blue background styling.
export const InvertedActiveStyle: Story = {
  args: {
    active: 'tool',
    counts: COUNTS,
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
