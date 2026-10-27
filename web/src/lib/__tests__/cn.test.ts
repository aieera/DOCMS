import { describe, it, expect } from 'vitest'
import { cn } from '@/lib/cn'

describe('cn()', () => {
  it('merges plain class strings', () => {
    expect(cn('a', 'b')).toBe('a b')
  })

  it('drops falsy values', () => {
    expect(cn('a', false, null, undefined, 'b')).toBe('a b')
  })

  it('dedupes tailwind conflicts via twMerge — last-writer wins', () => {
    expect(cn('p-2', 'p-4')).toBe('p-4')
    expect(cn('text-red-500', 'text-blue-500')).toBe('text-blue-500')
  })

  it('accepts conditional objects + arrays (clsx passthrough)', () => {
    expect(cn('base', { active: true, hidden: false }, ['extra'])).toBe('base active extra')
  })

  // The project's own utilities (globals.css `.text-caption`/`.text-metric`,
  // tailwind.config.js `shadow-focus-halo*`) are registered with
  // tailwind-merge. Unregistered, `text-caption` read as a text COLOUR, so a
  // later `text-muted-foreground` deleted it and the KPI hint rendered at 16px.
  describe('custom utilities', () => {
    it('keeps text-caption beside a text colour, in either order', () => {
      expect(cn('text-caption', 'text-muted-foreground')).toBe('text-caption text-muted-foreground')
      expect(cn('text-muted-foreground', 'text-caption')).toBe('text-muted-foreground text-caption')
      expect(cn('block min-h-4 truncate text-caption', 'text-destructive dark:text-foreground'))
        .toBe('block min-h-4 truncate text-caption text-destructive dark:text-foreground')
    })

    it('treats text-caption and text-metric as font sizes: the last size wins', () => {
      expect(cn('text-sm', 'text-caption')).toBe('text-caption')
      expect(cn('text-caption', 'text-sm')).toBe('text-sm')
      expect(cn('text-metric', 'text-caption')).toBe('text-caption')
      expect(cn('text-metric', 'text-foreground')).toBe('text-metric text-foreground')
    })

    it('treats the focus halos as box shadows, not shadow colours', () => {
      expect(cn('shadow-sm', 'shadow-focus-halo')).toBe('shadow-focus-halo')
      expect(cn('shadow-focus-halo', 'shadow-focus-halo-destructive')).toBe('shadow-focus-halo-destructive')
      expect(cn('focus-visible:shadow-focus-halo', 'aria-[invalid=true]:shadow-focus-halo-destructive'))
        .toBe('focus-visible:shadow-focus-halo aria-[invalid=true]:shadow-focus-halo-destructive')
      // A real shadow colour still sits beside a halo.
      expect(cn('shadow-focus-halo', 'shadow-red-500')).toBe('shadow-focus-halo shadow-red-500')
      expect(cn('shadow-xs', 'shadow-lg')).toBe('shadow-lg')
    })

    it('leaves page-title alone (it belongs to no group)', () => {
      expect(cn('page-title', 'text-foreground', 'text-sm', 'font-bold')).toBe('page-title text-foreground text-sm font-bold')
    })
  })
})
