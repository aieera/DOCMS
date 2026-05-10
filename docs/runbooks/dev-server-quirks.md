# Dev-server quirks (web/)

A short list of things that *only* bite the local Vite dev server.
Production builds aren't affected. If something looks weird after a
file rename or a fresh `git pull`, check here before debugging code.

## 504 "Outdated Optimize Dep" after `npm install`

**Symptom**: browser console shows `GET /node_modules/.vite/deps/<pkg>.js
net::ERR_ABORTED 504 (Outdated Optimize Dep)` for the package you just
added (e.g. `urql`, `@urql/exchange-persisted`).

**Cause**: Vite's dep pre-bundler scans `node_modules` once at boot
and writes `node_modules/.vite/deps/*.js`. A package added to
`package.json` while `npm run dev` is running isn't in that snapshot,
so the next import lands as a cache miss with an outdated hash.

**Fix**:

```powershell
Remove-Item -Recurse -Force node_modules\.vite ; npm run dev
```

Or one-shot: `npm run dev -- --force`.

**Prevention**: list the new package in `vite.config.ts` →
`optimizeDeps.include` so the pre-bundler always picks it up at
boot, even if a previous developer didn't restart cleanly.

## Tailwind sees stale utility set after a new file is added

**Symptom**: you add a new component file that uses a Tailwind utility
not present anywhere else in the source (e.g. `lg:max-w-screen-xl`,
some custom `bg-sidebar-accent/60`). The class shows up in the DOM
but renders unstyled. Hard-reloading the browser doesn't help.

**Cause**: the Tailwind v3 PostCSS plugin's content scan is built
once at dev-server boot. New files added after boot are picked up by
Vite's HMR, but the JIT class extraction can hold a stale glob cache
and skip them, leaving the utility undefined in the generated CSS.

**Fix**: restart Vite. `Ctrl+C` the `npm run dev` process and start it
again. The next boot picks up the new files and emits the utility.

**When you'll hit this**: only when *introducing* a new file with a
Tailwind class no other file uses. Editing existing files is fine —
HMR handles those normally.

## Sed inside double quotes silently drops `$variable`-shaped strings

**Symptom**: a `sed -i "s|foo|bar|g"` command run from a shell loop
silently zeroes out parts of the replacement that look like shell
variables (`$instanceId`, `$workspaceId`, etc.).

**Cause**: bash expands `$instanceId` inside double quotes *before*
sed sees it. Since the variable is unset, it expands to the empty
string, and sed receives a different pattern from what you typed.

**Fix**: use single quotes for the sed expression and escape any
literal single quotes inside the replacement, or escape the dollar
sign as `\$`. Single quotes are the boring-correct default here.

```bash
# Wrong — $instanceId disappears.
sed -i "s|/workflows/instances/$instanceId|/workflows/instances/$$instanceId|g" file.tsx

# Right — single quotes, no shell expansion.
sed -i 's|/workflows/instances/$instanceId|/workflows/instances/\$instanceId|g' file.tsx
```

## Windows checkout: case-only renames need a two-step git mv

**Symptom**: renaming `Button.tsx` → `button.tsx` (case-only) on a
Windows checkout appears to do nothing — `git status` is clean and
the original file is still on disk.

**Cause**: NTFS is case-insensitive by default, so `git mv Button.tsx
button.tsx` is a no-op from the filesystem's perspective.

**Fix**: rename through an intermediate name.

```powershell
git mv Button.tsx Button.tsx.tmp
git mv Button.tsx.tmp button.tsx
```

Or use `git config core.ignorecase false` in the repo and force the
rename with `git mv -f`. The intermediate-rename approach is less
likely to surprise other contributors.
