// Platform detection for keyboard-shortcut hints.
//
// Single source of truth: the topbar search chip and the dashboard
// quick-action card both label the same Cmd/Ctrl+K shortcut, and they
// used to disagree (the card hard-coded ⌘K, so Windows users were told
// the wrong key). Anything that renders a modifier glyph must come
// through here.
//
// navigator.platform is deprecated but is still the most reliable
// signal for this; navigator.userAgentData is uneven across browsers.
export function isMacPlatform(): boolean {
  return typeof navigator !== 'undefined' && /mac/i.test(navigator.platform)
}

/** The Cmd/Ctrl modifier as the user's own keyboard spells it. */
export function modifierKeyLabel(): string {
  return isMacPlatform() ? '⌘' : 'Ctrl'
}

/** Full shortcut label, e.g. "⌘K" on macOS and "Ctrl+K" elsewhere. */
export function shortcutLabel(key: string): string {
  return isMacPlatform() ? `⌘${key}` : `Ctrl+${key}`
}
