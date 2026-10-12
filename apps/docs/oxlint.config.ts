import { defineConfig } from "oxlint";
import astro from "ultracite/oxlint/astro";
import core from "ultracite/oxlint/core";
import react from "ultracite/oxlint/react";

const ignorePatterns = [
  ...(core.ignorePatterns ?? []),
  // These surfaces are owned by Starlight, their specialist formatters, or the
  // build that generated them. In particular, public prose must never be
  // mechanically rewritten as a side effect of a source-code maintenance pass.
  "**/*.css",
  "**/*.md",
  "public/**",
  ".astilba/env/**",
  "styled-system/**",
  "vendor/**",
  "src/env.d.ts",
];

export default defineConfig({
  // Type-aware Oxlint currently expects the TypeScript 7 toolchain. Keep this
  // first adoption syntax-aware while the site remains on TypeScript 6; Astro
  // and TypeScript compilation continue to run independently in `pnpm check`.
  // The Vitest style preset is also deferred: its global-import autofix treats
  // the Playwright suite as Vitest and can introduce invalid duplicate imports.
  extends: [core, astro, react],
  ignorePatterns,
  // Ultracite's correctness, security, import, React, hooks, and accessibility
  // rules stay enabled. These baseline exceptions are style preferences or
  // false positives for the existing Worker, generated-route, and test shapes;
  // Oxfmt owns mechanical consistency instead.
  rules: {
    "arrow-body-style": "off",
    complexity: "off",
    "default-case": "off",
    "import/consistent-type-specifier-style": "off",
    "jsx-a11y/prefer-tag-over-role": "off",
    "no-await-in-loop": "off",
    "no-bitwise": "off",
    "no-nested-ternary": "off",
    "no-use-before-define": "off",
    "oxc/branches-sharing-code": "off",
    "prefer-destructuring": "off",
    "prefer-named-capture-group": "off",
    "prefer-template": "off",
    "promise/avoid-new": "off",
    "promise/prefer-await-to-then": "off",
    "require-await": "off",
    "require-unicode-regexp": "off",
    "sort-keys": "off",
    "typescript/array-type": "off",
    "typescript/no-inferrable-types": "off",
    "unicorn/consistent-existence-index-check": "off",
    "unicorn/filename-case": "off",
    "unicorn/import-style": "off",
    "unicorn/no-array-sort": "off",
    "unicorn/no-await-expression-member": "off",
    "unicorn/no-nested-ternary": "off",
    "unicorn/no-useless-collection-argument": "off",
    "unicorn/no-useless-undefined": "off",
    "unicorn/numeric-separators-style": "off",
    "unicorn/prefer-code-point": "off",
    "unicorn/prefer-export-from": "off",
    "unicorn/prefer-math-trunc": "off",
    "unicorn/prefer-query-selector": "off",
    "unicorn/prefer-spread": "off",
    "unicorn/prefer-string-replace-all": "off",
    "unicorn/prefer-string-starts-ends-with": "off",
    "unicorn/switch-case-braces": "off",
    "unicorn/text-encoding-identifier-case": "off",
  },
  overrides: [
    {
      files: ["postcss.config.cjs"],
      rules: {
        "node/global-require": "off",
      },
    },
    {
      files: ["src/components/PageActions.tsx"],
      rules: {
        "react/react-compiler": "off",
      },
    },
    {
      files: ["src/components/ThemeToggle.astro"],
      rules: {
        // CSS durations include units (`250ms`, `0.25s`), so parseFloat is
        // intentional; Number would reject the valid value before unit scaling.
        "unicorn/prefer-number-coercion": "off",
      },
    },
    {
      files: [
        "scripts/smoke-production.mjs",
        "site/scripts/smoke-production.mjs",
      ],
      rules: {
        "no-promise-executor-return": "off",
        "no-throw-literal": "off",
        "no-unreachable-loop": "off",
      },
    },
  ],
});
