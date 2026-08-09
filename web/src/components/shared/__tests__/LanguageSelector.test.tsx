import { describe, it, expect, beforeAll, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'

import { LanguageSelector } from '../LanguageSelector'

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
