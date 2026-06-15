import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Dev server proxies /files → the Files BFF so the browser never needs CORS and
// the BFF stays the only thing that talks to SeDoc. Override the target with
// VITE_BFF_URL. In production the app is served behind the ERP and the BFF is
// reached at the same origin.
//
// Two HTML entry points are built:
//   - index.html : the standalone SPA (local dev / direct hosting)
//   - embed.html : the iframe-embeddable explorer (reads customerRef + apiBase
//                  from the URL, auth token via postMessage)
// The JS widget entry (mountFileExplorer) is built separately — see
// vite.widget.config.ts / `npm run build:widget`.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5180,
    proxy: {
      "/files": { target: process.env.VITE_BFF_URL || "http://localhost:8091", changeOrigin: true },
    },
  },
  build: {
    rollupOptions: {
      input: {
        main: fileURLToPath(new URL("./index.html", import.meta.url)),
        embed: fileURLToPath(new URL("./embed.html", import.meta.url)),
      },
    },
  },
});
