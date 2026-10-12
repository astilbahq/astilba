import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    coverage: {
      include: ["worker/**/*.ts"],
      provider: "istanbul",
    },
    include: ["test/**/*.test.ts"],
  },
});
