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
})
