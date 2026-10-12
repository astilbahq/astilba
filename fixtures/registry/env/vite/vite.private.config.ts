import { astilbaEnvBrowserBoundary } from "@astilba/env/vite";
import { defineConfig } from "vite";

export default defineConfig({
  build: {
    outDir: "rejected-dist",
    rolldownOptions: { input: "private-import.ts" },
  },
  plugins: [astilbaEnvBrowserBoundary()],
});
