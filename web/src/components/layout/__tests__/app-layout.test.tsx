import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'

import { AppLayout } from '../app-layout'
import { useUIStore } from '@/store/uiStore'

// The shell's offset logic is what is under test; the rail and header
// have their own suites, so they are stubbed to keep this one about the
// layout wrapper.
vi.mock('../app-sidebar', () => ({
  AppSidebar: () => <aside data-testid="sidebar-stub" />,
  MobileSidebar: () => null,
}))
vi.mock('../app-topbar', () => ({
  AppTopbar: () => <header data-testid="topbar-stub" />,
}))

// <main>'s parent is the padded wrapper that reserves room for the rail.
function contentColumn() {
  return screen.getByRole('main').parentElement as HTMLElement
}

beforeEach(() => {
  useUIStore.setState({ sidebarCollapsed: false })
})

describe('AppLayout offset', () => {
  it('reserves 240px for the rail from the shell breakpoint up', () => {
    render(<AppLayout><p>page</p></AppLayout>)
    const col = contentColumn()
    expect(col).toHaveClass('shell:ps-60')
    expect(col).not.toHaveClass('shell:ps-14')
    expect(col.className).not.toMatch(/\blg:ps-/)
  })

  it('reserves 56px when the rail is collapsed (the collapsed offset wins)', () => {
    useUIStore.setState({ sidebarCollapsed: true })
    render(<AppLayout><p>page</p></AppLayout>)
    const col = contentColumn()
    expect(col).toHaveClass('shell:ps-14')
    expect(col).not.toHaveClass('shell:ps-60')
    expect(col.className).not.toMatch(/\blg:ps-/)
  })

  it('renders the page inside the main landmark', () => {
    render(<AppLayout><p>page</p></AppLayout>)
    expect(screen.getByRole('main')).toHaveTextContent('page')
  })
})
