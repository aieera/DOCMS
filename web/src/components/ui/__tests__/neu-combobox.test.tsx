import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Combobox } from '@/components/ui/shadcn/combobox'

describe('neumorphic Combobox', () => {
  it('keeps the chosen option primary-highlighted even when cmdk marks it data-selected', async () => {
    render(
      <Combobox
        options={[
          { value: 'a', label: 'Alpha' },
          { value: 'b', label: 'Beta' },
        ]}
        value="b"
      />,
    )

    await userEvent.click(screen.getByRole('combobox'))
    const options = await screen.findAllByRole('option')
    const item = options.find((o) => o.textContent?.includes('Beta')) as HTMLElement
    expect(item).toBeTruthy()

    // cmdk's own base styling (data-[selected=true]:bg-accent, from
    // command.tsx, out of scope here) has higher specificity than a plain
    // bg-primary and would otherwise win whenever cmdk marks this row
    // data-selected="true" (hover or keyboard nav). The chosen row must
    // assert its own primary treatment under that same variant so it
    // survives.
    expect(item.className).toContain('bg-primary')
    expect(item.className).toContain('text-primary-foreground')
    expect(item.className).toContain('data-[selected=true]:bg-primary')
    expect(item.className).toContain('data-[selected=true]:text-primary-foreground')
  })

  it('does not apply the primary cue to a non-chosen option', async () => {
    render(
      <Combobox
        options={[
          { value: 'a', label: 'Alpha' },
          { value: 'b', label: 'Beta' },
        ]}
        value="b"
      />,
    )

    await userEvent.click(screen.getByRole('combobox'))
    const options = await screen.findAllByRole('option')
    const item = options.find((o) => o.textContent?.includes('Alpha')) as HTMLElement
    expect(item).toBeTruthy()
    expect(item.className).not.toContain('bg-primary')
    expect(item.className).not.toContain('data-[selected=true]:bg-primary')
  })
})
