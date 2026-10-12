import { defineConfig } from "oxfmt";
import ultracite from "ultracite/oxfmt";

export default defineConfig({
  ...ultracite,
  // Generated examples must remain byte-identical to their registry generator;
  // their own `generate --check` gate is stricter than source formatting.
  ignorePatterns: ["dist/**", "coverage/**", "examples/**/.astilba/env/**"],
});
