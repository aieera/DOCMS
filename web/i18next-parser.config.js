// ADR 0109 — i18next-parser config.
//
// Scans every .tsx/.ts in src/ for t('namespace:key') calls and
// writes the result to public/locales/{lng}/{ns}.json.
//
// Behaviour we deliberately opted into:
//   * `keepRemoved: true` — never delete a key the parser doesn't
//     see in this pass. Hand-translated Arabic copy MUST NOT be
//     stomped because a refactor temporarily removed a `t()` call.
//   * `createOldCatalogs: false` — no _old.json backup files; we
//     have git for that.
//   * `useKeysAsDefaultValue: false` — the parser fills `en` with
//     the literal English copy it sees inside `t('key', 'default')`
//     so a fresh key looks like `{ "key": "default" }` instead of
//     `{ "key": "key" }`. For keys without a default we still get
//     the key string back, which the regression Vitest catches.
//   * Namespace + key separator: ':' / '.'  — matches Prompt 1's
//     namespace list (common, auth, documents, admin, signatures,
//     intelligence, workflows, errors).
//
// Run via:  npm run i18n:extract
// CI guard: scripts/check-i18n-keys.mjs verifies every key the
// parser finds has both an English and an Arabic value.

export default {
  locales:           ['en', 'ar'],
  defaultNamespace:  'common',
  namespaceSeparator: ':',
  keySeparator:       '.',

  // What to scan. Clamp to first-party source + exclude tests so
  // synthetic `t('foo.bar')` fixtures in Vitest specs don't bleed
  // into the production bundle.
  input: [
    'src/**/*.{ts,tsx}',
    '!src/**/__tests__/**',
    '!src/**/*.test.{ts,tsx}',
    '!src/**/*.spec.{ts,tsx}',
    '!src/test/**',
    '!src/routeTree.gen.ts',
    '!src/generated/**',
  ],

  // Where to write extracted keys. Same shape as the existing bundle
  // tree so the HTTP backend picks them up without further config.
  output: 'public/locales/$LOCALE/$NAMESPACE.json',

  // Never destroy existing keys / values. The hand-translated Arabic
  // copy is the source of truth for `ar`; the parser only fills
  // English keys that don't exist yet.
  keepRemoved: true,
  createOldCatalogs: false,
  // Preserve already-translated values. When the parser sees a
  // `t('key')` call with no inline default, it falls back to the
  // existing value rather than wiping it. This is what makes the
  // command idempotent — running `npm run i18n:extract` after a
  // refactor never clobbers Arabic copy that's already been written.
  defaultValue(locale, _ns, _key, existing) {
    if (existing && typeof existing === 'string' && existing.length > 0) {
      return existing
    }
    // For brand-new keys we leave English copy empty; the howto's
    // workflow is "extract, then fill the en value manually with the
    // canonical English copy." Auto-generating keys-as-values
    // produces nonsense like `documents.upload.title:
    // documents.upload.title`.
    return ''
  },

  // Format JSON consistently with 2-space indent so diffs stay small.
  indentation: 2,

  // Match the lexer to the call shape we use everywhere:
  //   useTranslation('documents')
  //   t('documents:upload.title')
  //   t('upload.title', 'Upload')  ← default copy as 2nd arg
  lexers: {
    ts:  ['JavascriptLexer'],
    tsx: ['JsxLexer'],
  },

  // The `Trans` component isn't used in this repo today, but the
  // lexer is cheap to wire up so we don't need a config change when
  // it appears.
  reactNamespace: false,
}
