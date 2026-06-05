# i18n adoption — status, pattern, backlog

**Status (2026-06-05).** The i18n *infrastructure* is complete: `react-i18next`
wired in `web/src/i18n/index.ts`, namespace bundles under
`web/public/locales/{en,ar}/<namespace>.json` (admin, auth, common, documents,
errors, folders, intelligence, signatures), RTL handling, and a language
selector. **Adoption is partial** — ~23 of 257 components use `t()`.

This is a deliberately *scoped* effort (per the product call): migrate the
highest-traffic surfaces with **professional-quality Arabic** rather than a mass
machine-translated sweep that would degrade the existing bundles.

## Done so far
- **folders/** domain — fully migrated.
- **layout chrome** — `app-sidebar`, `app-topbar` (prior), plus this slice:
  `theme-toggle` (Light/Dark/System/Toggle), `WorkspaceSelector` (placeholder +
  load-error), `FolderTree` (manage-access, load-error, empty, smart-folders).
  Keys added to `common.json` (`theme.*`, `workspace_selector.*`,
  `folder_tree.*`) in **both en and ar**.

## Pattern (follow this for each component)
1. `const { t } = useTranslation('<namespace>')` — `common` for chrome;
   `documents` / `admin` / `intelligence` / … per domain.
2. Replace each user-facing literal with ``t('group.key') ?? 'English fallback'``.
   The `?? fallback` is load-bearing: `t()` can return null while a namespace
   lazy-loads (`ready === false`), so the fallback prevents a flash of the raw
   key. Reuse existing keys where they fit (e.g. `sidebar.smart_folders`).
3. Add the key to **both** `public/locales/en/<ns>.json` **and**
   `public/locales/ar/<ns>.json`. The `ar` bundles hold real, professional
   Arabic — match that quality; do **not** ship English-as-Arabic or
   machine output.
4. Verify: `node -e "JSON.parse(...)"` the bundles, `npx tsc --noEmit`,
   `npx vitest run src/i18n`.

## Backlog (remaining, roughly by size — each needs en + professionally-reviewed ar)
- **layout leftovers** — `breadcrumbs.tsx` (a ~20-entry admin route-label
  dictionary; really the admin domain), `auth-shell.tsx` (login-page marketing
  copy with technical terms like DEK/KEK — best done by a translator).
- **documents/** (~30 components), **intelligence/** (~15), **admin routes**
  (~40), **workflows/** (~15), **settings/** (~10), **shared/** + misc.
- **Arabic translation** for all new keys should be produced or reviewed by a
  professional translator to keep parity with the existing bundles. The
  mechanical `t()` migration can run ahead of that (English keys land first; ar
  values follow), but avoid committing fabricated Arabic.
