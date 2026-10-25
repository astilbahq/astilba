import { defineConfig } from "i18next-cli";

export default defineConfig({
  locales: ["en"],
  extract: {
    input: [
      "src/features/auth/**/*.{ts,tsx}",
      "src/components/{password-input,theme-menu,locale-menu}.tsx",
      "src/{router,hosting,ui}.tsx",
    ],
    output: "src/locales/{{language}}/{{namespace}}.json",
    defaultNS: "common",
    removeUnusedKeys: false,
  },
  types: {
    input: ["src/locales/en/*.json"],
    output: "src/i18n/i18next.d.ts",
    resourcesFile: "src/i18n/generated/resources.d.ts",
    enableSelector: true,
  },
  lint: {
    // These also contain dashboard screens, which migrate in later feature slices.
    ignore: ["src/router.tsx", "src/hosting.tsx"],
    acceptedTags: "all",
    checkTranslationPlaceholders: "error",
  },
});
