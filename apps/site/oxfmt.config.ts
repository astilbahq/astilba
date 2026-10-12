import { defineConfig } from "oxfmt";
import ultracite from "ultracite/oxfmt";

const ignorePatterns = [
  ...(ultracite.ignorePatterns ?? []),
  // Preserve authored prose and surfaces that already have framework-specific
  // formatting. This keeps formatter adoption out of the public content corpus.
  "**/*.astro",
  "**/*.css",
  "**/*.md",
  "public/**",
  ".astilba/env/**",
  "styled-system/**",
  "vendor/**",
  "src/env.d.ts",
];

export default defineConfig({
  ...ultracite,
  ignorePatterns,
});
