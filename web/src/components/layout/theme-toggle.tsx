import { Moon, Sun } from 'lucide-react'
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

export function ThemeToggle() {
  const { t } = useTranslation('common')
  const { mode, resolved, setMode } = useTheme()
  // The icon shows what is on screen (resolved), not the stored preference,
  // so "system" on a dark OS reads as a moon. The tooltip carries the
  // preference itself, since three states cannot be told apart by two icons.
  const Icon = resolved === 'light' ? Sun : Moon
  return (
    <Button
      variant="ghost"
      size="icon"
      onClick={() => setMode(NEXT_MODE[mode])}
      aria-label={t('theme.toggle')}
      title={`${t('theme.toggle')}: ${t(`theme.${mode}`)}`}
    >
      <Icon className="h-4 w-4" />
    </Button>
  )
}
