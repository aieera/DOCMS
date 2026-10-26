import { Monitor, Moon, Sun, type LucideIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useTheme, type ThemeMode } from './theme-provider'
import { Button } from '@/components/ui/shadcn/button'

// One click advances the stored preference: light -> dark -> system -> light.
// "system" is a step in the cycle (not a separate menu) so the OS-follow mode
// stays reachable without a dropdown.
const NEXT_MODE: Record<ThemeMode, ThemeMode> = {
  light: 'dark',
  dark: 'system',
  system: 'light',
}

// The icon shows the stored MODE, not the resolved theme. One step of the
// cycle (into or out of "system") always leaves the colours unchanged, so an
// icon that followed the resolved theme made that click look like it did
// nothing.
const MODE_ICON: Record<ThemeMode, LucideIcon> = {
  light: Sun,
  dark: Moon,
  system: Monitor,
}

export function ThemeToggle() {
  const { t } = useTranslation('common')
  const { mode, setMode } = useTheme()
  const Icon = MODE_ICON[mode]
  // The current mode is in the name ("Theme: System"), so a screen reader
  // hears each step too. One interpolated string, not two translations joined.
  const label = t('theme.toggle_with_mode', { mode: t(`theme.${mode}`) })
  return (
    <Button
      variant="ghost"
      size="icon"
      onClick={() => setMode(NEXT_MODE[mode])}
      aria-label={label}
      title={label}
    >
      <Icon className="h-4 w-4" aria-hidden />
    </Button>
  )
}
