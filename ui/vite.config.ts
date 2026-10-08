import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "path";
import { dark, light } from "./src/utils/tokens";

const apiTarget = process.env.IMS_API_PROXY_TARGET ?? "http://localhost:5020";

export default defineConfig({
  plugins: [
    react(),
    {
      name: "ella-index-html",
      transformIndexHtml(html: string) {
        return html
          .replaceAll("%CANVAS_LIGHT%", light.backgroundDefault)
          .replaceAll("%CANVAS_DARK%", dark.backgroundDefault);
      },
    },
  ],
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "src"),
    },
  },
  server: {
    port: 3020,
    proxy: {
      "/api": {
        target: apiTarget,
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 700,
    rolldownOptions: {
      output: {
        manualChunks(id) {
          if (
            id.includes("node_modules/react/") ||
            id.includes("node_modules/react-dom/") ||
            id.includes("@tanstack/")
          ) {
            return "vendor";
          }
          if (id.includes("@mui/x-")) {
            return "mui-x";
          }
          if (id.includes("@mui/") || id.includes("@emotion/")) {
            return "mui";
          }
        },
      },
    },
  },
});
