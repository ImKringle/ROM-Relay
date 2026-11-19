import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The build is embedded in the Go binary (internal/web/spa). Bundles are served
// from /app-assets/, kept apart from /assets/ (the existing static images and
// fonts). `npm run dev` proxies the API to a relay on :8080.
export default defineConfig({
  base: "/app-assets/",
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "../internal/web/spa/dist",
    emptyOutDir: true,
    assetsDir: ".",
    // No inline scripts or styles, so the CSP stays script-src 'self'.
    assetsInlineLimit: 0,
    sourcemap: false,
  },
  server: {
    proxy: Object.fromEntries(
      ["/api", "/healthz", "/media", "/d", "/qr", "/assets", "/sprites", "/download"].map((p) => [p, "http://127.0.0.1:8080"]),
    ),
  },
});
