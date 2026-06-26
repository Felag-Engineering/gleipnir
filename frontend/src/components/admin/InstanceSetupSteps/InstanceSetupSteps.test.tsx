import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { InstanceSetupSteps } from './InstanceSetupSteps'
import { deriveSetupSteps, humanizeHealthDetail } from '@/utils/instanceSetup'

const fullInput = {
  authStrategy: 'oauth2_authcode',
  configSchema: { required: ['app_level_token'] },
  hasSubscriptionSchema: true,
}

describe('InstanceSetupSteps', () => {
  it('renders nothing when there are no steps', () => {
    const { container } = render(<InstanceSetupSteps steps={[]} onNavigate={() => {}} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('shows progress count and a row per step (all incomplete)', () => {
    const steps = deriveSetupSteps({ ...fullInput, credentials: undefined, config: {}, subscriptionScope: {} })
    render(<InstanceSetupSteps steps={steps} onNavigate={() => {}} />)

    expect(screen.getByText('0/3')).toBeInTheDocument()
    expect(screen.getByText('Add credentials')).toBeInTheDocument()
    expect(screen.getByText(/Fill required config/)).toBeInTheDocument()
    expect(screen.getByText('Set subscription scope')).toBeInTheDocument()
    // every step incomplete → three "not done" icons
    expect(screen.getAllByLabelText('not done')).toHaveLength(3)
  })

  it('marks the first incomplete blocking step as Next', () => {
    const steps = deriveSetupSteps({
      ...fullInput,
      credentials: { strategy: 'oauth2_authcode', has_token: true },
      config: {},
      subscriptionScope: {},
    })
    render(<InstanceSetupSteps steps={steps} onNavigate={() => {}} />)
    // credentials done → config is Next
    const next = screen.getByText('Next')
    expect(next).toBeInTheDocument()
    expect(screen.getByText('1/3')).toBeInTheDocument()
    expect(screen.getByLabelText('done')).toBeInTheDocument()
  })

  it('renders the humanized health detail with a deep-link CTA', async () => {
    const user = userEvent.setup()
    const onNavigate = vi.fn()
    const steps = deriveSetupSteps({ ...fullInput, credentials: undefined, config: {}, subscriptionScope: {} })
    render(
      <InstanceSetupSteps
        steps={steps}
        healthDetail={humanizeHealthDetail('config_missing')}
        onNavigate={onNavigate}
      />,
    )
    expect(screen.getByText(/Add the required configuration/)).toBeInTheDocument()
    // Both the detail CTA and the config step CTA read "Go to Config" and both
    // deep-link to the config tab — click the first and assert the navigation.
    await user.click(screen.getAllByRole('button', { name: 'Go to Config' })[0])
    expect(onNavigate).toHaveBeenCalledWith('config')
  })

  it('navigates to the step tab when a CTA is clicked', async () => {
    const user = userEvent.setup()
    const onNavigate = vi.fn()
    const steps = deriveSetupSteps({ ...fullInput, credentials: undefined, config: {}, subscriptionScope: {} })
    render(<InstanceSetupSteps steps={steps} onNavigate={onNavigate} />)

    await user.click(screen.getByRole('button', { name: /Go to Credentials/ }))
    expect(onNavigate).toHaveBeenCalledWith('credentials')
  })

  it('shows all steps done with no CTAs when complete', () => {
    const steps = deriveSetupSteps({
      ...fullInput,
      credentials: { strategy: 'oauth2_authcode', has_token: true },
      config: { app_level_token: '***' },
      subscriptionScope: { channels: ['C1'] },
    })
    render(<InstanceSetupSteps steps={steps} onNavigate={() => {}} />)
    expect(screen.getByText('3/3')).toBeInTheDocument()
    expect(screen.getAllByLabelText('done')).toHaveLength(3)
    expect(screen.queryByRole('button')).toBeNull()
    expect(screen.queryByText('Next')).toBeNull()
  })
})
