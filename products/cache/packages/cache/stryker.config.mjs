const config = {
  packageManager: "pnpm",
  // REQUIRED in this workspace: @astilba/harness is a real (un-sandboxed)
  // sibling package whose OWN node_modules/@astilba/cache symlink resolves
  // back to this package's real (un-mutated) source — Stryker's default
  // sandbox only symlinks node_modules for the package under test (cwd),
  // not for workspace-linked siblings, so a sandboxed run would exercise the
  // harness's `world()`/`createCache()` against the REAL src while mutating
  // an inert copy (every mutant silently "no-coverage"). `inPlace` mutates
  // the real files directly (Stryker backs them up and restores them itself
  // on completion/interruption) so there is exactly one copy of the package
  // on disk and the harness's symlink lands on the mutated source.
  inPlace: true,
  // Explicit bare specifier (not the "@stryker-mutator/*" glob default):
  // pnpm's isolated node_modules does not hoist vitest-runner as a sibling
  // of core inside core's own .pnpm dependency directory, which is where
  // Stryker's glob-based plugin loader looks. A bare specifier instead goes
  // through normal Node resolution from this package, which succeeds.
  plugins: ["@stryker-mutator/vitest-runner"],
  testRunner: "vitest",
  vitest: {
    configFile: "./stryker-vitest.config.mjs",
    // The sacred suite (54 tests) is the kill oracle in full for every mutant —
    // vitest-runner's default "related" mode narrows to a module-graph subset
    // per mutated file, which is the wrong semantics here (and fails outright:
    // it can't relate test files to src files through this repo's import shape).
    related: false,
  },

  mutate: [
    "src/**/*.ts",
    "!src/**/*.test.ts",
    "!src/**/*.d.ts",
    "!src/adapters/**",
  ],
  // Keep the existing mutation-score floor unchanged during migration.
  thresholds: { break: 55, high: 80, low: 60 },
  ignorePatterns: ["../harness/**"],
  reporters: ["html", "clear-text", "progress"],
  coverageAnalysis: "perTest",
  tempDirName: "stryker-tmp",
};

export default config;
