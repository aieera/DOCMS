import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Library build of the embeddable widget: emits dist-widget/sedoc-file-explorer.js
// (+ .css) exposing `mountFileExplorer`. React/ReactDOM are bundled in so the host
// (the ERP) can drop in a single self-contained script:
//
//   import { mountFileExplorer } from "sedoc-file-explorer";
//   mountFileExplorer(el, { customerRef, apiBase, getAuthToken });
//
// Build with: npm run build:widget
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist-widget",
    lib: {
      entry: fileURLToPath(new URL("./src/widget.tsx", import.meta.url)),
      name: "SedocFileExplorer",
      formats: ["es", "umd"],
      fileName: (format) => `sedoc-file-explorer.${format}.js`,
    },
  },
});
