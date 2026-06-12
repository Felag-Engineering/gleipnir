import { useState } from 'react'
import type { Meta, StoryObj } from '@storybook/react-vite'
import { fn } from 'storybook/test'
import '@/tokens.css'
import { ContactTray } from './ContactTray'

const meta: Meta<typeof ContactTray> = {
  title: 'Components/ContactTray',
  component: ContactTray,
  args: {
    onClose: fn(),
  },
  parameters: {
    layout: 'fullscreen',
  },
}

export default meta
type Story = StoryObj<typeof ContactTray>

/** The tray is closed — renders nothing over the shell. */
export const Closed: Story = {
  args: {
    open: false,
  },
}

/** The tray is open, showing the email, bug-report, and ask-a-question channels. */
export const Open: Story = {
  args: {
    open: true,
  },
}

/** Interactive demo: a trigger button opens and closes the tray, mirroring the sidebar usage. */
export const Interactive: Story = {
  render: (args) => {
    function Demo() {
      const [open, setOpen] = useState(false)
      return (
        <>
          <button type="button" onClick={() => setOpen(true)}>
            Open contact tray
          </button>
          <ContactTray open={open} onClose={() => { args.onClose(); setOpen(false) }} />
        </>
      )
    }
    return <Demo />
  },
}
