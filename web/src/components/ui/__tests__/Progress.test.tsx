import { describe, it, expect, afterEach } from 'vitest'
import { act, render, screen } from '@testing-library/react'
import i18n from 'i18next'

import { Progress } from '../shadcn/progress'
import arCommon from '../../../../public/locales/ar/common.json'

const indicator = () => (screen.getByRole('progressbar').firstElementChild as HTMLElement)

afterEach(async () => {
  await act(async () => { await i18n.changeLanguage('en') })
})

describe('Progress', () => {
  it('fills from the left in LTR', () => {
    render(<Progress value={30} aria-label="Share" />)
    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '30')
    expect(indicator().style.transform).toBe('translateX(-70%)')
  })

  it('fills from the right in RTL', async () => {
    i18n.addResourceBundle('ar', 'common', arCommon, true, true)
    await act(async () => { await i18n.changeLanguage('ar') })
    render(<Progress value={30} aria-label="Share" />)
    expect(indicator().style.transform).toBe('translateX(70%)')
  })

  it('treats a missing value as empty and clamps out-of-range values', () => {
    const { rerender } = render(<Progress aria-label="Share" />)
    expect(indicator().style.transform).toBe('translateX(-100%)')
    rerender(<Progress value={140} aria-label="Share" />)
    expect(indicator().style.transform).toBe('translateX(0%)')
    rerender(<Progress value={-20} aria-label="Share" />)
    expect(indicator().style.transform).toBe('translateX(-100%)')
  })
})
