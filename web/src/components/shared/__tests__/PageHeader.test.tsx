import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { PageHeader } from '@/components/shared/PageHeader'

describe('<PageHeader>', () => {
  it('renders title and description', () => {
    render(<PageHeader title="Users" description="Manage members" />)
    expect(screen.getByRole('heading', { level: 1, name: 'Users' })).toBeInTheDocument()
    expect(screen.getByText('Manage members')).toBeInTheDocument()
  })

  it('renders action slot when provided', () => {
    render(<PageHeader title="X" actions={<button>Invite</button>} />)
    expect(screen.getByRole('button', { name: 'Invite' })).toBeInTheDocument()
  })

  it('omits the description paragraph when not provided', () => {
    const { container } = render(<PageHeader title="X" />)
    // Only the h1 should exist (no <p>).
    expect(container.querySelector('p')).toBeNull()
  })
})
