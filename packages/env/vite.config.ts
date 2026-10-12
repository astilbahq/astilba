import { defineConfig } from "vite-plus";

import oxfmtConfig from "./oxfmt.config.ts";
import oxlintConfig from "./oxlint.config.ts";

export default defineConfig({
  fmt: oxfmtConfig,
  // Vite+ owns the typed lane. Standalone Ultracite consumes the same format
  // and rule policy, but its adapter does not reproduce Vite+'s compiler pass.
  lint: {
    ...oxlintConfig,
    options: {
      typeAware: true,
      typeCheck: true,
    },
  },
  pack: {
    clean: true,
    dts: true,
    entry: {
      "browser/index": "src/browser/index.ts",
      "cli/astilba-env": "src/cli/astilba-env.ts",
      "cli/compile": "src/cli/compile.ts",
      index: "src/index.ts",
      "runtime/index": "src/runtime/index.ts",
      "runtime/workerd": "src/runtime/workerd.ts",
      "vite/index": "src/vite/index.ts",
    },
    format: "esm",
    outExtensions: () => ({ dts: ".d.ts", js: ".js" }),
    root: "src",
    sourcemap: false,
    tsconfig: "tsconfig.pack.json",
    unbundle: true,
  },
  test: {
    coverage: {
      include: ["src/**/*.ts"],
      provider: "istanbul",
      reporter: ["text", "json-summary"],
    },
    include: ["src/**/*.test.ts", "test/**/*.test.ts"],
    pool: "forks",
    sequence: {
      concurrent: false,
    },
    testTimeout: 30_000,
  },
});
