import { defineConfig } from "oxfmt";
import ultracite from "ultracite/oxfmt";

export default defineConfig({
  ...ultracite,
  ignorePatterns: [
    ...(ultracite.ignorePatterns ?? []),
    "**/dist/**",
    "**/styled-system/**",
    "packages/env/**",
    "products/cache/**",
    "apps/docs/**",
    "apps/site/**",
    "fixtures/registry/**",
  ],
});
