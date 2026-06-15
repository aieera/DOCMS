import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Dev server proxies /files → the Files BFF so the browser never needs CORS and
// the BFF stays the only thing that talks to SeDoc. Override the target with
// VITE_BFF_URL. In production the app is served behind the ERP and the BFF is
// reached at the same origin.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5180,
    proxy: {
      "/files": { target: process.env.VITE_BFF_URL || "http://localhost:8091", changeOrigin: true },
    },
  },
});
