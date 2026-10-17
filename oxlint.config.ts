import { defineConfig } from "oxlint";
import core from "ultracite/oxlint/core";

export default defineConfig({
  extends: [core],
  ignorePatterns: [
    ...(core.ignorePatterns ?? []),
    "**/dist/**",
    "**/styled-system/**",
    "packages/env/**",
    "packages/cache/**",
    "packages/cache-harness/**",
    "apps/cache-demo/**",
    "fixtures/cache-react-router/**",
    "apps/docs/**",
    "apps/site/**",
    "fixtures/registry/**",
  ],
});
