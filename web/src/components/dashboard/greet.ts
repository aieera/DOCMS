import type { TFunction } from 'i18next'

/** The dashboard h1: a time-of-day greeting, plus the first name when there is one. */
export function greet(t: TFunction<'common'>, name: string | undefined): string {
  const hour = new Date().getHours()
  const window =
    hour < 5 ? t('dashboard.greeting.up_late')
      : hour < 12 ? t('dashboard.greeting.morning')
      : hour < 18 ? t('dashboard.greeting.afternoon')
      : t('dashboard.greeting.evening')
  // The comma is part of the phrase, not glue: Arabic uses "،" (U+060C),
  // so joining with a Latin "," here would leave one wrong mark in an
  // otherwise translated greeting.
  return name ? t('dashboard.greeting.with_name', { greeting: window, name }) : window
}
