import { fileURLToPath } from "node:url";

import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: [
      {
        find: /^@astilba\/ui$/u,
        replacement: fileURLToPath(
          new URL("../../packages/ui/src/index.ts", import.meta.url)
        ),
      },
    ],
  },
  test: {
    environment: "jsdom",
    include: ["tests/**/*.test.tsx"],
  },
});
