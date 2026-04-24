// Conventional commits. Enforced on PRs via .github/workflows/commitlint.yml.
//
// Subject scope is the service name (or a slash-prefixed top-level
// like "web", "mobile", "ci", "docs"). release-please maps
// `feat(storage): ...` → storage minor; `fix(storage): ...` → storage
// patch. Umbrella releases pick up commits without a scope.

module.exports = {
  extends: ['@commitlint/config-conventional'],
  rules: {
    'type-enum': [2, 'always', [
      'feat', 'fix', 'perf', 'refactor', 'docs', 'test',
      'build', 'ci', 'chore', 'revert',
    ]],
    'scope-enum': [1, 'always', [
      'auth', 'policy', 'document', 'storage', 'search', 'workflow',
      'notification', 'audit', 'signature', 'billing', 'connector',
      'collaboration', 'intelligence', 'preview',
      'web', 'mobile',
      'ci', 'deploy', 'docs', 'release', 'deps',
      'pkg', 'proto',
    ]],
    'subject-case': [2, 'never', ['pascal-case', 'upper-case']],
    'subject-empty': [2, 'never'],
    'subject-max-length': [2, 'always', 100],
    'header-max-length': [2, 'always', 120],
  },
}
