import { describe, it, expect, afterEach } from 'vitest'
import { isMacPlatform, modifierKeyLabel, shortcutLabel } from '@/lib/platform'

// The topbar chip and the dashboard "Search documents" card label the
// same shortcut; the card used to hard-code ⌘K, so Windows users were
// told a key that does nothing.
function setPlatform(value: string) {
  Object.defineProperty(window.navigator, 'platform', { value, configurable: true })
}

afterEach(() => setPlatform(''))

describe('shortcutLabel', () => {
  it('uses Ctrl off macOS', () => {
    setPlatform('Win32')
    expect(isMacPlatform()).toBe(false)
    expect(modifierKeyLabel()).toBe('Ctrl')
    expect(shortcutLabel('K')).toBe('Ctrl+K')
  })

  it('uses the command glyph on macOS', () => {
    setPlatform('MacIntel')
    expect(isMacPlatform()).toBe(true)
    expect(modifierKeyLabel()).toBe('⌘')
    expect(shortcutLabel('K')).toBe('⌘K')
  })
})
