import { defineConfig } from "oxfmt";
import ultracite from "ultracite/oxfmt";

export default defineConfig({
  ...ultracite,
  ignorePatterns: [
    ...(ultracite.ignorePatterns ?? []),
    "**/dist/**",
    "**/styled-system/**",
    "packages/env/**",
    "apps/docs/**",
    "apps/site/**",
    "fixtures/registry/**",
    "tooling/migration/install-baselines/**",
    "tooling/migration/workflows/**",
    "docs/architecture/verification-inventory.json",
  ],
});
