// This package is ESM ("type": "module"). Node 22.12+ lets Tailwind's CJS
// loader require() this file natively, where `require` is not defined, so
// the plugins are imported here instead of required inline.
import animate from 'tailwindcss-animate'
import rtl from 'tailwindcss-rtl'
import typography from '@tailwindcss/typography'

// A colour token from globals.css. The tokens are plain colour strings
// (hex / rgb()), so `var(--x)` is the colour itself; the color-mix wrapper
// only exists so Tailwind's opacity modifiers (`bg-primary/10`,
// `hover:bg-primary/90`) keep working — at the default alpha of 1 it
// resolves to exactly `var(--x)`.
const token = (name) => `color-mix(in srgb, var(--${name}) calc(<alpha-value> * 100%), transparent)`

/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  darkMode: 'class',
  theme: {
    container: {
      center: true,
      padding: '2rem',
      screens: { '2xl': '1400px' },
    },
    extend: {
      // Faces are self-hosted via @fontsource imports in src/styles/globals.css.
      fontFamily: {
        sans: ['Geist', 'system-ui', 'sans-serif'],
        mono: ['Geist Mono', 'ui-monospace', 'monospace'],
      },
      colors: {
        // Canonical shadcn semantic tokens — plain colour strings in
        // globals.css, mapped here by name. Use as `bg-background`,
        // `text-foreground`, `border-border`, `bg-primary
        // text-primary-foreground`, etc.
        border: token('border'),
        input: token('input'),
        ring: token('ring'),
        background: token('background'),
        foreground: token('foreground'),
        'outline-bg': token('outline-bg'),
        primary: {
          DEFAULT: token('primary'),
          foreground: token('primary-foreground'),
        },
        secondary: {
          DEFAULT: token('secondary'),
          foreground: token('secondary-foreground'),
        },
        destructive: {
          DEFAULT: token('destructive'),
          foreground: token('destructive-foreground'),
        },
        success: {
          DEFAULT: token('success'),
          foreground: token('success-foreground'),
        },
        warning: {
          DEFAULT: token('warning'),
          foreground: token('warning-foreground'),
          strong: token('warning-strong'),
        },
        info: {
          DEFAULT: token('info'),
          foreground: token('info-foreground'),
        },
        muted: {
          DEFAULT: token('muted'),
          foreground: token('muted-foreground'),
        },
        accent: {
          DEFAULT: token('accent'),
          foreground: token('accent-foreground'),
        },
        popover: {
          DEFAULT: token('popover'),
          foreground: token('popover-foreground'),
        },
        card: {
          DEFAULT: token('card'),
          foreground: token('card-foreground'),
        },
        sidebar: {
          DEFAULT: token('sidebar'),
          foreground: token('sidebar-foreground'),
          border: token('sidebar-border'),
          accent: token('sidebar-accent'),
          'accent-foreground': token('sidebar-accent-foreground'),
        },
        chart: {
          1: token('chart-1'),
          2: token('chart-2'),
          3: token('chart-3'),
          4: token('chart-4'),
          5: token('chart-5'),
        },
      },
      borderRadius: {
        sm: 'var(--radius-sm)',
        md: 'var(--radius-md)',
        lg: 'var(--radius-lg)',
        xl: 'var(--radius-xl)',
        '2xl': '18px',
        '3xl': '24px',
        full: '9999px',
      },
      boxShadow: {
        xs: 'var(--shadow-xs)',
        sm: 'var(--shadow-sm)',
        md: 'var(--shadow-md)',
        lg: 'var(--shadow-lg)',
        // Neumorphic-era names, kept as aliases of the flat elevation
        // scale so routes not yet restyled still compile. Remove once
        // nothing uses shadow-neu* / shadow-float*.
        neu: 'var(--shadow-sm)',
        'neu-sm': 'var(--shadow-xs)',
        'neu-inset': 'none',
        'neu-pressed': 'none',
        'neu-lg': 'var(--shadow-md)',
        float: 'var(--shadow-lg)',
        'float-sm': 'var(--shadow-md)',
      },
      screens: {
        shell: '801px',
        wide: '1181px',
      },
      keyframes: {
        'accordion-down': {
          from: { height: '0' },
          to: { height: 'var(--radix-accordion-content-height)' },
        },
        'accordion-up': {
          from: { height: 'var(--radix-accordion-content-height)' },
          to: { height: '0' },
        },
        'fade-in': {
          '0%': { opacity: '0' },
          '100%': { opacity: '1' },
        },
        'slide-in-from-top': {
          '0%': { transform: 'translateY(-4px)', opacity: '0' },
          '100%': { transform: 'translateY(0)', opacity: '1' },
        },
      },
      animation: {
        'accordion-down': 'accordion-down 0.2s ease-out',
        'accordion-up': 'accordion-up 0.2s ease-out',
        'fade-in': 'fade-in 0.15s ease-out',
        'slide-in-from-top': 'slide-in-from-top 0.2s ease-out',
      },
    },
  },
  // ADR 0107 — i18n RTL.
  //   * tailwindcss-rtl adds `rtl:` / `ltr:` variants AND a `space-s-*`
  //     direction-aware horizontal stack utility used where flex+gap
  //     isn't a clean swap.
  //   * Tailwind 3.4 already ships the bulk of the logical utilities
  //     natively (`ms-*`, `me-*`, `ps-*`, `pe-*`, `start-*`, `end-*`,
  //     `border-s/e`, `rounded-s/e`, `text-start/end`); the plugin
  //     supplements rather than duplicates them.
  plugins: [animate, rtl, typography],
}
