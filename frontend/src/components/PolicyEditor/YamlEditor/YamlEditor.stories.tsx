import type { Meta, StoryObj } from '@storybook/react-vite'
import { useState } from 'react'
import '@/tokens.css'
import { YamlEditor } from './YamlEditor'
import decoratorStyles from './YamlEditor.stories.module.css'

const VALID_YAML = `name: my-policy
trigger:
  type: webhook
task: |
  Check for new deployments and summarize status.
capabilities:
  tools:
    - tool: k8s.list_deployments
`

const INVALID_YAML = `name: broken-policy
  trigger:
type: webhook   # inconsistent indentation
  task: |
unclosed: [bracket
`

const meta: Meta<typeof YamlEditor> = {
  title: 'PolicyEditor/YamlEditor',
  component: YamlEditor,
  decorators: [
    (Story) => (
      <div className={decoratorStyles.storyDecorator}>
        <Story />
      </div>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof YamlEditor>

// Wrapper to make it stateful for Storybook
function StatefulYamlEditor(props: { initialValue: string; readOnly?: boolean }) {
  const [value, setValue] = useState(props.initialValue)
  return (
    <YamlEditor
      value={value}
      onChange={setValue}
      onValidityChange={() => {}}
      readOnly={props.readOnly}
    />
  )
}

export const ValidYaml: Story = {
  render: () => <StatefulYamlEditor initialValue={VALID_YAML} />,
}

export const InvalidYaml: Story = {
  render: () => <StatefulYamlEditor initialValue={INVALID_YAML} />,
}

export const ReadOnly: Story = {
  render: () => <StatefulYamlEditor initialValue={VALID_YAML} readOnly />,
}
