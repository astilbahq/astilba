/**
 * Stryker mutation testing config for @astilba/cache ( toolchain.md
 * "Mutation" row — wired at RED→GREEN, own CI job).
 *
 * Mutates only this package's kernel source and runs the sacred invariant +
 * scenario suite (vitest `invariants` project) as the kill signal — the
 * `unit` project (harness self-tests) is deliberately NOT the mutation
 * target here; it exists to validate the harness fakes, not src/.
 *
 * Kept as a plain, un-annotated object literal on purpose: a `@ts-check` plus a
 * `PartialStrykerOptions` `@type` JSDoc (whose type lives in the `api` package)
 * would pull a THIRD Stryker package into devDependencies just to satisfy a doc
 * comment under the type-aware lint pass — the toolchain standard pins only
 * `core` + `vitest-runner`. Stryker does not need the type to run.
 */
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
  // Mutate only what the kill oracle can REACH. The oracle is the sacred suite, which is
  // fakes-only by design and can never exercise `src/adapters/**` — the
  // real Cloudflare boundary is adjudicated by conformance + integration instead. Mutating
  // it yields nothing but "no coverage" mutants: measured 2026-07-10, the adapters
  // contributed 202 mutants, 0 killed, dragging the total from 60.58% to 50.27%. A ratchet
  // over unreachable code ratchets DOWNWARD every time phase 2 adds a driver, which would
  // train us to lower the bar. Scope it to the kernel core.
  //
  // THIS EXCLUSION IS LOAD-BEARING, NOT AN OVERSIGHT — DO NOT "FIX" IT. It is rule 11's
  // arithmetic. The adapters' assurance budget is conformance + integration + the ground
  // rule 13 same-PR teeth; NEVER mutation. Adding `src/adapters/**` back to the mutant set
  // does not increase assurance by one mutant — the fakes-only oracle cannot execute a line
  // of it — it only makes the ratchet fall on every honest driver.
  mutate: [
    "src/**/*.ts",
    "!src/**/*.test.ts",
    "!src/**/*.d.ts",
    "!src/adapters/**",
  ],
  // The RATCHET floor (toolchain.md tag ritual (f)). Not an aspiration: behavioral tests
  // pin observables, not internals, so a high score is not the goal — a score that never
  // FALLS is. Raise it deliberately when coverage genuinely improves; never lower it to
  // make a run pass.
  //
  // Baseline measured 2026-07-10 @ e7786bc over the scoped set above: 60.58% total /
  // 74.90% covered (524 killed, 255 timeout, 261 survived, 246 no-coverage).
  //
  // The margin is not padding. The score is TIMEOUT-SENSITIVE, hence load-dependent: two
  // runs over identical source produced identical `killed` (524) and an identical
  // `survived + timeout` set (516), yet 31 mutants migrated between `survived` and
  // `timeout` — and Stryker scores a timeout as a kill. The number therefore moves several
  // points on machine load alone. `break` sits below that noise so the ratchet fails on a
  // real regression, never on a busy runner.
  thresholds: { break: 55, high: 80, low: 60 },
  ignorePatterns: ["../harness/**"],
  reporters: ["html", "clear-text", "progress"],
  coverageAnalysis: "perTest",
  tempDirName: "stryker-tmp",
};

export default config;
