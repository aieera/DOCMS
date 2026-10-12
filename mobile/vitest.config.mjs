// Plain-object config (no `vitest/config` import) so the suite also runs
// from a sibling install: ../web/node_modules/.bin/vitest run --root .
// Native modules (expo-*, netinfo, react-native-*) are vi.mock'ed in the
// tests; nothing here needs a device.
export default {
  // tsconfig.json extends expo/tsconfig.base, which only exists after an
  // install; give the transformer an inline tsconfig so tests run without one.
  oxc: { tsconfig: false },
  test: {
    globals: true,
    environment: 'node',
    include: ['__tests__/**/*.test.ts'],
  },
}
