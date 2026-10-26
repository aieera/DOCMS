import { clsx, type ClassValue } from 'clsx'
import { extendTailwindMerge } from 'tailwind-merge'

// tailwind-merge only knows Tailwind's own scale. Anything else it sorts by
// prefix, so `text-caption` read as a text COLOUR and a later
// `text-muted-foreground` deleted it (the KPI hint rendered at 16px instead
// of the 12/16 caption). Register the project's own utilities in their real
// groups: `.text-caption` / `.text-metric` (globals.css) set the font size,
// and `shadow-focus-halo*` (tailwind.config.js boxShadow) are box shadows.
// `page-title` matches no prefix rule, so it is in no group and never merged.
const twMerge = extendTailwindMerge({
  extend: {
    classGroups: {
      'font-size': [{ text: ['caption', 'metric'] }],
      shadow: [{ shadow: ['focus-halo', 'focus-halo-destructive'] }],
    },
  },
})

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
