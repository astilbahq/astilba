import { defineConfig } from "oxfmt";
import ultracite from "ultracite/oxfmt";

export default defineConfig({
  ...ultracite,
  ignorePatterns: [
    ...(ultracite.ignorePatterns ?? []),
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
