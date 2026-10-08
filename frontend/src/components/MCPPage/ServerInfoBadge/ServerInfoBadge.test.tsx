import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { ServerInfoBadge } from './ServerInfoBadge'

describe('ServerInfoBadge', () => {
  it('renders the reported name and version', () => {
    render(<ServerInfoBadge info={{ name: 'acme-mcp', version: '2.3.1' }} />)
    expect(screen.getByText('acme-mcp 2.3.1')).toBeInTheDocument()
  })

  it('renders only the name when the version is empty', () => {
    render(<ServerInfoBadge info={{ name: 'acme-mcp', version: '' }} />)
    expect(screen.getByText('acme-mcp')).toBeInTheDocument()
  })

  it('renders nothing when the server reported no identity', () => {
    const { container } = render(<ServerInfoBadge info={null} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('renders markup in a hostile name as inert text', () => {
    const { container } = render(
      <ServerInfoBadge info={{ name: '<img src=x onerror=alert(1)>', version: '1' }} />,
    )
    expect(container.querySelector('img')).toBeNull()
    expect(screen.getByText('<img src=x onerror=alert(1)> 1')).toBeInTheDocument()
  })
})
