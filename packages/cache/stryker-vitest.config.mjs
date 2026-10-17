import { defineConfig } from "vitest/config";

// Mutation-testing-only vitest config (consumed by @stryker-mutator/vitest-runner
// via stryker.config.mjs's `vitest.configFile`). The pinned vitest-runner@9.6.1
// has no per-project filter (it loads every project in the resolved config and
// runs all of them — see VitestTestRunner#init/#run, which iterates
// `this.ctx.projects` unconditionally), so a multi-project root config would let
// mutants be killed by the `unit` project too. This file mirrors ONLY the
// `invariants` project's test globs from vite.config.ts (single project,
// no `unit`) so mutants of packages/cache/src are exercised exclusively against
// the sacred invariant + scenario suite, per this sweep's mandate.
export default defineConfig({
  test: {
    environment: "node",
    include: ["test/{invariants,scenarios}/**/*.test.ts"],
    exclude: ["**/test/bun-smoke/**", "**/node_modules/**"],
    // Resolved through node_modules (not a "../harness" filesystem-relative
    // path): Stryker's sandbox for this package only contains packages/cache's
    // own tree plus a symlinked node_modules, not a sibling packages/cache-harness
    // checkout, so a path reaching outside the sandbox 404s. The workspace
    // symlink at node_modules/@astilba/harness resolves back to the real
    // package either way.
    setupFiles: ["./node_modules/@astilba/harness/src/fc-setup.ts"],
  },
});
