import { useState } from 'react'
import type { Meta, StoryObj } from '@storybook/react'
import { Tabs, panelId, tabId, type TabDescriptor } from './Tabs'

const TABS: TabDescriptor[] = [
  { id: 'basics', label: 'Basics' },
  { id: 'trigger', label: 'Trigger' },
  { id: 'capabilities', label: 'Capabilities' },
  { id: 'modelLimits', label: 'Model & Limits' },
]

// Interactive wrapper so the controlled Tabs component has somewhere to store
// the active id inside Storybook. Renders a matching panel to demonstrate the
// aria-controls / aria-labelledby wiring.
function TabsDemo({ tabs, initial = 'basics' }: { tabs: TabDescriptor[]; initial?: string }) {
  const [active, setActive] = useState(initial)
  return (
    <div style={{ maxWidth: 720 }}>
      <Tabs tabs={tabs} activeId={active} onChange={setActive} ariaLabel="Demo tabs" idPrefix="demo" />
      {tabs.map((t) => (
        <div
          key={t.id}
          role="tabpanel"
          id={panelId('demo', t.id)}
          aria-labelledby={tabId('demo', t.id)}
          hidden={active !== t.id}
          style={{ padding: 16 }}
        >
          {t.label} panel content
        </div>
      ))}
    </div>
  )
}

const meta: Meta<typeof Tabs> = {
  title: 'Components/Tabs',
  component: Tabs,
}

export default meta

type Story = StoryObj<typeof Tabs>

export const Default: Story = {
  render: () => <TabsDemo tabs={TABS} />,
}

export const WithErrorBadges: Story = {
  render: () => (
    <TabsDemo
      tabs={[
        { id: 'basics', label: 'Basics', errorCount: 1 },
        { id: 'trigger', label: 'Trigger' },
        { id: 'capabilities', label: 'Capabilities', errorCount: 3 },
        { id: 'modelLimits', label: 'Model & Limits' },
      ]}
    />
  ),
}

export const StartsOnLastTab: Story = {
  render: () => <TabsDemo tabs={TABS} initial="modelLimits" />,
}
