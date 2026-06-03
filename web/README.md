# SeDoc web

React 18 + TanStack Router + TanStack Query + Vite 5 + TypeScript.

## Local setup

```bash
cp .env.example .env.local
# Open .env.local and fill in SEDOC_GATEWAY_SECRET. Generate a
# fresh value with: openssl rand -hex 32
# Use the SAME value in scripts/run-all-services.sh.
npm install
npm run dev
```

The Vite dev server refuses to start if `SEDOC_GATEWAY_SECRET` is
unset — the Go services require it on every request via
`pkg/middleware.RequireGatewaySignature`. See [`vite.config.ts`](./vite.config.ts).

## Scripts

- `npm run dev` — start the dev server on :3000 with the proxy to the
  Go services (host mode by default; gateway mode when
  `VITE_PROXY_MODE=gateway` or `VITE_GATEWAY_URL` is set).
- `npm run build` — production SPA build under `dist/`.
- `npm test` — unit tests via vitest.
- `npm run lint` — eslint over `src/`.
- `npx tsc --noEmit` — type check.
