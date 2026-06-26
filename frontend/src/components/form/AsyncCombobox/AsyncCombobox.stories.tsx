import type { Meta, StoryObj } from '@storybook/react'
import { useState } from 'react'
import '@/tokens.css'
import type { ApiPluginOption } from '@/api/types'
import { AsyncCombobox } from './AsyncCombobox'

// Fake Slack channels for story demonstrations.
const CHANNELS: ApiPluginOption[] = [
  { value: 'C001', label: '#general', group: 'Joined' },
  { value: 'C002', label: '#engineering', group: 'Joined' },
  { value: 'C003', label: '#design', group: 'Joined' },
  { value: 'C004', label: '#marketing (not joined)', group: 'Not joined', disabled: true },
  { value: 'C005', label: '#finance (not joined)', group: 'Not joined', disabled: true },
]

// fakeSearch simulates a 400ms network round trip and filters by query substring.
function fakeSearch(query: string): Promise<ApiPluginOption[]> {
  return new Promise((resolve) => {
    setTimeout(() => {
      const q = query.toLowerCase()
      resolve(
        q
          ? CHANNELS.filter((c) => c.label.toLowerCase().includes(q))
          : CHANNELS,
      )
    }, 400)
  })
}

// Wrapper that holds the controlled value so the stories can be interactive.
function Controlled(props: Partial<React.ComponentProps<typeof AsyncCombobox>> & { initialValue?: string }) {
  const { initialValue = '', ...rest } = props
  const [value, setValue] = useState<string | string[]>(initialValue)
  const displayValue = Array.isArray(value) ? value.join(', ') : value
  return (
    <div style={{ width: 320, padding: '16px' }}>
      <label
        htmlFor="demo"
        style={{ display: 'block', fontSize: 13, color: 'var(--text-primary)', marginBottom: 4 }}
      >
        Slack channel
      </label>
      <AsyncCombobox
        id="demo"
        value={value}
        onChange={setValue}
        onSearch={fakeSearch}
        placeholder="Search channels…"
        {...rest}
      />
      <p
        style={{ marginTop: 8, fontSize: 12, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)' }}
      >
        value: {displayValue || '(none)'}
      </p>
    </div>
  )
}

const meta: Meta<typeof Controlled> = {
  title: 'Form/AsyncCombobox',
  component: Controlled,
  parameters: {
    backgrounds: { default: 'dark' },
  },
}

export default meta

type Story = StoryObj<typeof Controlled>

// Default: empty selection, fully interactive (type to filter).
export const Default: Story = {}

// Pre-selected: value already set.
export const PreSelected: Story = {
  args: {
    initialValue: 'C001',
  },
}

// Degraded: plugin options provider unavailable — falls back to plain text input.
export const Degraded: Story = {
  args: {
    degraded: true,
  },
}

// Disabled: input entirely locked.
export const Disabled: Story = {
  args: {
    disabled: true,
    initialValue: 'C002',
  },
}
