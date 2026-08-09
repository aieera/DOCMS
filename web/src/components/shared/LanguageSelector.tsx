// LanguageSelector — ADR 0106 i18n topbar control.
//
// On change: flip i18n, stamp <html dir/lang>, persist server-side.
// Order matters: changeLanguage first so the UI re-renders the new
// strings immediately even if the backend write later 4xxs (we toast
// the error but don't roll back — a session-only language switch is
// still useful, and the next /auth/me will reconcile).
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Languages } from 'lucide-react'

// Labels stay in the NATIVE script regardless of the current i18n
// language. Picking your own language from a dropdown labelled in a
// language you don't speak is the classic locale-switcher trap; the
// native rendering means a user who landed in the wrong locale can
// always recognise their own language in the list.

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/shadcn/select'
import { updateLocale } from '@/api/auth'
import { SUPPORTED_LOCALES, applyDocumentDir, type SupportedLocale } from '@/i18n'
import { useAuthStore } from '@/store/authStore'

const LABELS: Record<SupportedLocale, string> = {
  en: 'English',
  ar: 'العربية',
}

export function LanguageSelector() {
  const { i18n, t } = useTranslation('common')
  const updateUser = useAuthStore((s) => s.updateUser)
  const isAuthed   = useAuthStore((s) => s.isAuthenticated)
  // i18n.language may include a region tag (e.g. 'en-US'); the select
  // keys on the base language. resolvedLanguage strips the region for
  // us but is NOT always populated — it stays undefined on init paths
  // that never run a changeLanguage. Falling straight through to 'en'
  // there made the picker disagree with the rest of the app (which
  // derives direction from i18n.language — see hooks/useDirection): the
  // UI rendered Arabic RTL while the trigger read "English" and the
  // active locale lost its checkmark. Mirror useDirection's resolution
  // order, then keep the value inside the supported set so Radix always
  // has a matching item.
  const detected = (i18n.resolvedLanguage ?? i18n.language ?? 'en').split('-')[0]
  const current = (
    SUPPORTED_LOCALES.includes(detected as SupportedLocale) ? detected : 'en'
  ) as SupportedLocale

  const onChange = async (val: string) => {
    const locale = val as SupportedLocale
    if (!SUPPORTED_LOCALES.includes(locale)) return
    await i18n.changeLanguage(locale)
    applyDocumentDir(locale)
    // Optimistically reflect the new locale in the auth store so
    // anything reading user.locale (e.g. the locale-aware date
    // formatter) updates without waiting for a /auth/me refresh.
    updateUser({ locale })
    if (!isAuthed) return     // pre-login picker — nothing to persist.
    try {
      await updateLocale(locale)
    } catch (e) {
      toast.error('Could not save your language preference. We kept the new language for this session.')
      // Don't revert i18n — the user explicitly clicked.
      console.warn('updateLocale failed', e)
    }
  }

  return (
    <Select value={current} onValueChange={onChange}>
      <SelectTrigger className="w-[110px] gap-1.5 border-white/15 bg-white/10 text-white hover:bg-white/15 focus:ring-white/30 [&_svg]:text-white/70" aria-label={t('language.label')}>
        <Languages className="h-4 w-4 opacity-60" />
        <SelectValue />
      </SelectTrigger>
      <SelectContent align="end">
        {SUPPORTED_LOCALES.map((l) => (
          <SelectItem key={l} value={l}>{LABELS[l]}</SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
