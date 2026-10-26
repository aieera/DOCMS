import { describe, it, expect, beforeAll, afterEach, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'

import { LanguageSelector } from '../LanguageSelector'
import { updateLocale } from '@/api/auth'
import { useAuthStore } from '@/store/authStore'

vi.mock('@/api/auth', () => ({ updateLocale: vi.fn() }))

// i18next leaves `resolvedLanguage` undefined on init paths that never
// run a changeLanguage — reading it alone made the picker fall through
// to English while the rest of the UI was already Arabic, so the active
// locale lost its checkmark and the trigger named the wrong language.
beforeAll(async () => {
  await i18n.use(initReactI18next).init({
    lng: 'ar',
    fallbackLng: 'en',
    supportedLngs: ['en', 'ar'],
    resources: { en: { common: {} }, ar: { common: {} } },
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  })
})

describe('LanguageSelector', () => {
  it('marks the active locale as selected when Arabic is active', async () => {
    expect(i18n.resolvedLanguage).toBeUndefined()
    expect(i18n.language).toBe('ar')

    render(<LanguageSelector />)
    expect(screen.getByRole('combobox')).toHaveTextContent('العربية')

    await userEvent.click(screen.getByRole('combobox'))
    const [english, arabic] = screen.getAllByRole('option')
    expect(arabic).toHaveAttribute('aria-selected', 'true')
    expect(english).toHaveAttribute('aria-selected', 'false')
  })
})

describe('LanguageSelector trigger', () => {
  afterEach(async () => {
    await i18n.changeLanguage('ar')
    useAuthStore.setState({ user: null, isAuthenticated: false })
    vi.mocked(updateLocale).mockReset()
  })

  // Header control: a 36px ghost (no border, transparent until hovered) with
  // the language icon, the current language's own-script label and the
  // Select's chevron.
  it('is a 36px ghost trigger with the icon, the current label and a chevron', () => {
    render(<LanguageSelector />)
    const trigger = screen.getByRole('combobox')
    expect(trigger).toHaveTextContent('العربية')
    expect(trigger).toHaveAttribute('aria-label', i18n.t('language.label'))
    expect(trigger).toHaveClass(
      'h-9',
      'w-auto',
      'rounded-md',
      'px-2',
      'gap-1.5',
      'text-sm',
      'border-0',
      'bg-transparent',
      'hover:bg-accent',
      'focus-visible:ring-1',
      'focus-visible:ring-ring',
      'focus-visible:shadow-focus-halo',
    )
    // No border and no filled surface of the old 110px bordered Select.
    expect(trigger).not.toHaveClass('border')
    expect(trigger).not.toHaveClass('bg-muted')
    expect(trigger).not.toHaveClass('bg-background')
    expect(trigger).not.toHaveClass('w-[110px]')
    expect(trigger).not.toHaveClass('focus-visible:ring-2')
    const icons = Array.from(trigger.querySelectorAll('svg'))
    expect(icons[0]).toHaveClass('lucide-languages', 'h-4', 'w-4')
    expect(icons[icons.length - 1]).toHaveClass('lucide-chevron-down')
  })

  it('still switches the language, stamps <html dir> and persists the choice', async () => {
    useAuthStore.setState({
      user: { id: 'u-1', email: 'a@x', role: 'owner', tenant_id: 't-1', locale: 'ar' } as never,
      isAuthenticated: true,
    })
    render(<LanguageSelector />)
    await userEvent.click(screen.getByRole('combobox'))
    await userEvent.click(screen.getByRole('option', { name: 'English' }))

    expect(i18n.language).toBe('en')
    expect(document.documentElement.dir).toBe('ltr')
    expect(useAuthStore.getState().user?.locale).toBe('en')
    expect(updateLocale).toHaveBeenCalledWith('en')
    expect(screen.getByRole('combobox')).toHaveTextContent('English')
  })
})

