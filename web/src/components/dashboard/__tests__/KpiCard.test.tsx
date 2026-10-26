import { describe, it, expect, vi } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import i18n from 'i18next'
import userEvent from '@testing-library/user-event'
import { Bell, FileText, Stamp } from 'lucide-react'

import { KpiCard } from '../KpiCard'
import arCommon from '../../../../public/locales/ar/common.json'

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  Link: ({ children, to, search, ...rest }:
    { children: React.ReactNode; to?: string; search?: Record<string, string> } & Record<string, unknown>) =>
    <a href={search ? `${to}?${new URLSearchParams(search).toString()}` : to} {...rest}>{children}</a>,
}))

// Fourteen months: the first two (100 each) fall outside the 12-bar window,
// so if they were drawn the scale would be 100, not 10.
const series = [100, 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 5].map((value, i) => ({
  label: `M${i}`, iso: `2025-01-${String(i + 1).padStart(2, '0')}T00:00:00.000Z`, value,
}))

describe('KpiCard', () => {
  it('renders a link named "{label}: {value}, {hint}" for the href variant', () => {
    render(<KpiCard icon={FileText} label="Documents" value={4} hint="across 1 workspace" href="/workspaces" />)
    const link = screen.getByRole('link', { name: 'Documents: 4, across 1 workspace' })
    expect(link).toHaveAttribute('href', '/workspaces')
  })

  it('carries search params on the link', () => {
    render(
      <KpiCard icon={Stamp} label="Awaiting approval" value={1} hint="waiting since today"
        href="/tasks" search={{ tab: 'approvals' }} />,
    )
    expect(screen.getByRole('link', { name: 'Awaiting approval: 1, waiting since today' }))
      .toHaveAttribute('href', '/tasks?tab=approvals')
  })

  it('renders a button with the same accessible name for the onClick variant, and no link', async () => {
    const onClick = vi.fn()
    render(<KpiCard icon={Bell} label="Unread" value={3} hint="notifications" onClick={onClick} />)
    const button = screen.getByRole('button', { name: 'Unread: 3, notifications' })
    expect(button).toHaveAttribute('type', 'button')
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
    await userEvent.click(button)
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('renders kpi-failed, and a name that says unavailable, when isError is set', () => {
    const { container } = render(
      <KpiCard icon={Bell} label="Unread" value={undefined} isError hint="unable to load" onClick={vi.fn()} />,
    )
    expect(container.querySelector('[data-testid="kpi-failed"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="kpi-skeleton"]')).toBeNull()
    expect(screen.getByRole('button', { name: 'Unread: unavailable' })).toBeInTheDocument()
  })

  it('names a failed card "unavailable" in Arabic too', async () => {
    i18n.addResourceBundle('ar', 'common', arCommon, true, true)
    await act(async () => { await i18n.changeLanguage('ar') })
    try {
      render(<KpiCard icon={Bell} label="غير المقروءة" value={undefined} isError hint="x" onClick={vi.fn()} />)
      const name = arCommon.dashboard.kpi.unavailable_aria.replace('{{label}}', 'غير المقروءة')
      expect(screen.getByRole('button', { name })).toBeInTheDocument()
      expect(name).not.toMatch(/unavailable/)
    } finally {
      await act(async () => { await i18n.changeLanguage('en') })
    }
  })

  it('is a bordered 14px-radius card with the small shadow, a hover border and the focus halo — never neumorphic', () => {
    render(<KpiCard icon={FileText} label="Documents" value={4} href="/workspaces" />)
    const card = screen.getByRole('link')
    expect(card).toHaveClass('rounded-xl', 'border', 'border-border', 'bg-card', 'shadow-sm', 'p-5')
    expect(card.className).toMatch(/hover:border-/)
    expect(card).toHaveClass('focus-visible:shadow-focus-halo')
    expect(card.className).not.toMatch(/shadow-neu/)
  })

  // The 12/16 caption must survive cn(): an unregistered `text-caption` was
  // merged away by the tone colour, so the hint rendered at the body size and
  // the card grew by 8px when data arrived.
  it('renders the hint in the caption style next to its tone colour', () => {
    const { rerender } = render(
      <KpiCard icon={FileText} label="Documents" value={4} hint="across 1 workspace" href="/workspaces" />,
    )
    expect(screen.getByTestId('kpi-hint')).toHaveClass('text-caption', 'text-muted-foreground', 'min-h-4')
    rerender(
      <KpiCard icon={FileText} label="Open tasks" value={2} hint="1 overdue" hintTone="alert" href="/tasks" />,
    )
    expect(screen.getByTestId('kpi-hint')).toHaveClass('text-caption', 'text-destructive')
  })

  it('renders the numeral in the metric style', async () => {
    render(<KpiCard icon={FileText} label="Documents" value={7} href="/workspaces" />)
    expect(await screen.findByText('7', {}, { timeout: 2000 })).toHaveClass('text-metric')
  })

  it('draws the last 12 points of the series as bars scaled to the largest', () => {
    const { container } = render(
      <KpiCard icon={FileText} label="Documents" value={4} sparkline={series} href="/workspaces" />,
    )
    const slot = container.querySelector('[data-testid="kpi-sparkline"]')
    expect(slot).toHaveClass('flex', 'h-6', 'items-end', 'gap-0.5')
    expect(slot).toHaveAttribute('aria-hidden', 'true')
    const bars = container.querySelectorAll<HTMLElement>('[data-testid="kpi-sparkline-bar"]')
    expect(bars).toHaveLength(12)
    expect([...bars].map((b) => b.style.height))
      .toEqual(['0%', '10%', '20%', '30%', '40%', '50%', '60%', '70%', '80%', '90%', '100%', '50%'])
  })
})
