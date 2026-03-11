import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { CopyBlock } from './CopyBlock'

const meta: Meta<typeof CopyBlock> = {
  title: 'Shared/CopyBlock',
  component: CopyBlock,
}

export default meta
type Story = StoryObj<typeof CopyBlock>

export const Default: Story = {
  args: {
    text: 'Hello, world!',
    children: (
      <pre style={{ margin: 0, padding: '8px 12px', background: 'var(--bg-elevated)', borderRadius: 4 }}>
        Hello, world!
      </pre>
    ),
  },
}

export const MultiLineCode: Story = {
  args: {
    text: 'const x = 1\nconst y = 2\nreturn x + y',
    children: (
      <pre style={{ margin: 0, padding: '8px 12px', background: 'var(--bg-elevated)', borderRadius: 4, fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>
        {`const x = 1\nconst y = 2\nreturn x + y`}
      </pre>
    ),
  },
}
