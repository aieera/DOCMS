// Private vs shared.
//
// The trap this pins: member_count is NOT a sharing signal. Creating a
// workspace auto-enrols the creator, so every workspace reports one member
// whether or not anyone else can see it — reading "1 member" as "shared"
// would label every private workspace shared, which is the exact failure
// that makes such a badge worse than none.

import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'

import { WorkspaceAccessBadge } from '../WorkspaceAccessBadge'

describe('<WorkspaceAccessBadge>', () => {
  it('reads Private when nobody else has access', () => {
    render(<WorkspaceAccessBadge sharedWithCount={0} memberCount={1} />)
    expect(screen.getByText('Private')).toBeInTheDocument()
  })

  it('reads Shared with the count once someone else has access', () => {
    const { container } = render(<WorkspaceAccessBadge sharedWithCount={3} memberCount={4} />)
    expect(container.textContent).toContain('Shared')
    expect(container.textContent).toContain('3')
    expect(screen.queryByText('Private')).not.toBeInTheDocument()
  })

  it('is Private for a grant-only workspace with a single member', () => {
    // The creator alone. Trusting member_count here would say "shared".
    render(<WorkspaceAccessBadge sharedWithCount={0} memberCount={1} />)
    expect(screen.getByText('Private')).toBeInTheDocument()
  })

  it('is Shared when access comes from a grant and adds no member row', () => {
    // member_count stays 1 (creator only); the grant is what shares it.
    const { container } = render(<WorkspaceAccessBadge sharedWithCount={1} memberCount={1} />)
    expect(container.textContent).toContain('Shared')
  })

  it('falls back to member_count on responses that predate shared_with_count', () => {
    render(<WorkspaceAccessBadge memberCount={1} />)
    expect(screen.getByText('Private')).toBeInTheDocument()

    const { container } = render(<WorkspaceAccessBadge memberCount={5} />)
    expect(container.textContent).toContain('Shared')
    expect(container.textContent).toContain('4') // 5 members minus the creator
  })

  it('explains itself on hover rather than relying on the icon alone', () => {
    const { container: priv } = render(<WorkspaceAccessBadge sharedWithCount={0} />)
    expect(priv.querySelector('[title]')?.getAttribute('title')).toMatch(/only you/i)

    const { container: shared } = render(<WorkspaceAccessBadge sharedWithCount={1} />)
    expect(shared.querySelector('[title]')?.getAttribute('title')).toMatch(/1 person has/i)
  })

  // protobuf int64 → JSON string. This is the shape the real API sends, so
  // it is the shape most worth testing: `"1" === 1` is false, which is
  // exactly why the meta row next to this badge used to read "1 members".
  it('handles the string counts the API actually returns', () => {
    render(<WorkspaceAccessBadge sharedWithCount={'0'} memberCount={'1'} />)
    expect(screen.getByText('Private')).toBeInTheDocument()

    const { container } = render(<WorkspaceAccessBadge sharedWithCount={'3'} memberCount={'1'} />)
    expect(container.textContent).toContain('Shared')
    expect(container.textContent).toContain('3')
  })

  it('falls back correctly when only a string member_count is present', () => {
    const { container } = render(<WorkspaceAccessBadge memberCount={'5'} />)
    expect(container.textContent).toContain('4')
  })

  it('never renders a negative count', () => {
    const { container } = render(<WorkspaceAccessBadge memberCount={0} />)
    expect(screen.getByText('Private')).toBeInTheDocument()
    expect(container.textContent).not.toContain('-')
  })
})
