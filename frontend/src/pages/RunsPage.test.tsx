import { render, screen } from '@testing-library/react'
import RunsPage from './RunsPage'

it('renders without crashing', () => {
  render(<RunsPage />)
  expect(screen.getByRole('heading', { name: /runs/i })).toBeInTheDocument()
})
