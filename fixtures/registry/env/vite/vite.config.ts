import { astilbaEnvBrowserBoundary } from "@astilba/env/vite";
import { defineConfig } from "vite";

export default defineConfig({
  build: { sourcemap: true },
  plugins: [astilbaEnvBrowserBoundary()],
});
