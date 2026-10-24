import { defineConfig } from "vite-plus";

const generated = [
  "**/node_modules/**",
  "**/.artifacts/**",
  "**/styled-system/**",
  "**/dist/**",
  "**/src/api/generated/**",
];

export default defineConfig({
  lint: {
    ignorePatterns: generated,
    plugins: ["typescript", "unicorn", "oxc", "react", "jsx-a11y", "import"],
    categories: { correctness: "error" },
    options: { typeAware: true, typeCheck: true },
    rules: {
      "import/no-cycle": "error",
      "react/rules-of-hooks": "error",
      "react/no-danger": "error",
      "react/jsx-no-target-blank": ["error", { allowReferrer: true }],
      "no-script-url": "error",
      // Compiler-specific constraints are not part of this app's React setup.
      "react/refs": "off",
      "react/purity": "off",
      "react/set-state-in-effect": "off",
      // Live status containers and labelled SVGs intentionally use ARIA roles.
      "jsx-a11y/prefer-tag-over-role": "off",
      "typescript/no-explicit-any": "error",
      "jsx-a11y/no-noninteractive-tabindex": "error",
    },
  },
  fmt: {
    ignorePatterns: [...generated, "**/assets/**"],
    printWidth: 80,
    tabWidth: 2,
    useTabs: false,
    semi: true,
    singleQuote: false,
    sortImports: { newlinesBetween: false, sortSideEffects: false },
  },
});
