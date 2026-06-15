/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        // Inherit the ERP's palette via CSS variables so the module feels native.
        brand: "var(--brand, #2563eb)",
      },
    },
  },
  plugins: [],
};
