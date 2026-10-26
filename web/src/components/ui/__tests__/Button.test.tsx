import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Button, buttonVariants } from '@/components/ui/shadcn/button'

describe('<Button>', () => {
  it('fires onClick when enabled', async () => {
    const user = userEvent.setup()
    const spy = vi.fn()
    render(<Button onClick={spy}>Save</Button>)
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('is non-clickable when loading', async () => {
    const user = userEvent.setup()
    const spy = vi.fn()
    render(<Button loading onClick={spy}>Save</Button>)
    const btn = screen.getByRole('button')
    expect(btn).toBeDisabled()
    await user.click(btn)
    expect(spy).not.toHaveBeenCalled()
  })

  it('disabled prop blocks clicks', async () => {
    const user = userEvent.setup()
    const spy = vi.fn()
    render(<Button disabled onClick={spy}>Save</Button>)
    await user.click(screen.getByRole('button'))
    expect(spy).not.toHaveBeenCalled()
  })

  it('applies destructive variant class', () => {
    render(<Button variant="destructive">Delete</Button>)
    // buttonVariants destructive = `bg-destructive text-destructive-foreground …`
    // (semantic token), not a raw `bg-red-500`.
    expect(screen.getByRole('button')).toHaveClass('bg-destructive')
  })

  it('forwards arbitrary className', () => {
    render(<Button className="my-extra-class">x</Button>)
    expect(screen.getByRole('button')).toHaveClass('my-extra-class')
  })

  it('renders the outline variant on the outline surface with the xs shadow', () => {
    render(<Button variant="outline">Cancel</Button>)
    const btn = screen.getByRole('button')
    expect(btn).toHaveClass('bg-outline-bg', 'shadow-xs', 'border', 'border-border', 'hover:bg-accent')
  })

  it('default variant is the flat primary fill with a /90 hover', () => {
    render(<Button>Go</Button>)
    expect(screen.getByRole('button')).toHaveClass(
      'bg-primary',
      'text-primary-foreground',
      'hover:bg-primary/90',
    )
  })

  it('ghost variant is transparent until hovered', () => {
    render(<Button variant="ghost">More</Button>)
    const btn = screen.getByRole('button')
    expect(btn).toHaveClass('hover:bg-accent')
    expect(btn.className).not.toMatch(/(^|\s)bg-/)
  })

  it('default size is 36px tall', () => {
    render(<Button>Go</Button>)
    expect(screen.getByRole('button')).toHaveClass('h-9', 'px-4')
  })

  it('sm size is 32px tall', () => {
    render(<Button size="sm">Go</Button>)
    expect(screen.getByRole('button')).toHaveClass('h-8', 'px-3')
  })

  it('icon size is a 36px square and icon-sm a 32px square', () => {
    const { rerender } = render(<Button size="icon" aria-label="a" />)
    expect(screen.getByRole('button')).toHaveClass('h-9', 'w-9')
    rerender(<Button size="icon-sm" aria-label="a" />)
    expect(screen.getByRole('button')).toHaveClass('h-8', 'w-8')
  })

  it('uses the 8px radius token', () => {
    render(<Button>Go</Button>)
    expect(screen.getByRole('button')).toHaveClass('rounded-md')
  })

  it('focus-visible draws a 1px ring plus a 4px ring/50 halo', () => {
    render(<Button>Go</Button>)
    const btn = screen.getByRole('button')
    expect(btn).toHaveClass('focus-visible:ring-1', 'focus-visible:ring-ring')
    expect(btn).toHaveClass('focus-visible:shadow-focus-halo')
  })

  it('no variant carries a neumorphic shadow or a brightness hover', () => {
    for (const variant of ['default', 'destructive', 'outline', 'secondary', 'ghost', 'link'] as const) {
      expect(buttonVariants({ variant })).not.toMatch(/shadow-neu|brightness/)
    }
  })

  it('secondary and destructive are flat fills with a /80 and /90 hover', () => {
    const { rerender } = render(<Button variant="secondary">x</Button>)
    expect(screen.getByRole('button')).toHaveClass('bg-secondary', 'text-secondary-foreground', 'hover:bg-secondary/80')
    rerender(<Button variant="destructive">x</Button>)
    expect(screen.getByRole('button')).toHaveClass('bg-destructive', 'text-destructive-foreground', 'hover:bg-destructive/90')
  })

  it('link variant stays an underlined text button', () => {
    render(<Button variant="link">Docs</Button>)
    expect(screen.getByRole('button')).toHaveClass('text-primary', 'hover:underline')
  })

  it('asChild renders the child element carrying the button classes', () => {
    render(
      <Button asChild variant="outline">
        <a href="/x">Open</a>
      </Button>,
    )
    const link = screen.getByRole('link', { name: 'Open' })
    expect(link).toHaveClass('bg-outline-bg', 'h-9')
  })
})
