import type { Meta, StoryObj } from '@storybook/react'
import { FieldError } from './FieldError'

const meta: Meta<typeof FieldError> = {
  title: 'Form/FieldError',
  component: FieldError,
}

export default meta

type Story = StoryObj<typeof FieldError>

export const Empty: Story = {
  args: {
    messages: [],
  },
}

export const Single: Story = {
  args: {
    id: 'field-name-error',
    messages: 'Name is required',
  },
}

export const Multi: Story = {
  args: {
    id: 'field-tool-error',
    messages: [
      'Tool must use dot notation (server.tool_name)',
      'Tool name is a duplicate',
    ],
  },
}
