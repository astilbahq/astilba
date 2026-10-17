import { fileURLToPath } from "node:url";

import ultraciteFmt from "ultracite/oxfmt";
import core from "ultracite/oxlint/core";
import jsPlugins from "ultracite/oxlint/js-plugins";
import vitest from "ultracite/oxlint/vitest";
import { defineConfig } from "vite-plus";
import type { OxlintConfig } from "vite-plus/lint";

// The ONE sanctioned `as unknown as` cast in this repo. vite-plus
// bundles its own copy of oxlint, so the `OxlintConfig` type it exports is
// nominally skewed from the type the ultracite presets are authored against — a
// types-only mismatch, identical config shape at runtime. No other `as unknown as`
// is permitted anywhere in the tree.
// A trailing config whose file-scoped override disables the two vitest rules that
// false-positive on @fast-check/vitest's mandated `test.prop(...)` — it must be a
// peer of the presets in `extends` (not a top-level `rules` entry) so its
// file-scoped override outranks the vitest preset's own file-scoped override.
const testPropOverride = {
  overrides: [
    {
      files: ["**/*.{test,spec}.{ts,tsx,js,jsx}"],
      plugins: ["vitest"],
      rules: {
        // `test.prop`'s `expect`s are inside a real (fast-check) test block.
        "vitest/no-standalone-expect": "off",
        // `test` comes from @fast-check/vitest, not "vitest"; its autofix
        // otherwise corrupts the import by adding a second `test` from vitest.
        "vitest/prefer-importing-vitest-globals": "off",
      },
    },
  ],
};

// The phase-2 conformance lane (rule 11/12): parameterized driver contracts. Two
// vitest false-positives for THAT shape, scoped to the conformance test files only.
const conformanceOverride = {
  overrides: [
    {
      files: ["test/conformance/**/*.{test,spec}.ts"],
      plugins: ["vitest"],
      rules: {
        // The per-interface wiring files register their `it`s via an imported
        // parameterized runner, so no literal test appears in-file (false positive).
        "sonarjs/no-empty-test-file": "off",
        // Teeth meta-assertions assert only THAT a wrong driver rejects — never on an
        // exact message (messages would make the proofs brittle).
        "vitest/require-to-throw-message": "off",
      },
    },
  ],
};

// These two rules autofix `toBe(false)`/`toBe(true)` into `toBeFalsy()`/`toBeTruthy()`. Under
// a serve reports `durable` as `true` / `false` / ABSENT, so known-not-durable and unknowable are
// DIFFERENT answers and `toBeFalsy` accepts both — the rewrite turns a proof of the exact value
// into a tautology that passes on `undefined`. Their premise ("truthiness is what you mean") does
// not hold where the exact value IS the claim, so they are repealed repo-wide rather than fenced
// per file: a per-file fence protects only the files it names, and the failure it misses is green.
// They are right about ordinary booleans, which is the whole of what turning them off costs.
//
// The demo's own `toBe(false)` assertions (the scene catalogue proving that arming ONE fault touched
// NOTHING ELSE — `expect(dials.kv.failReads).toBe(false)` on a `boolean` dial) fall under this same
// repeal: its glob covers every test file, `apps/cache-demo` included, so no demo-scoped copy is needed.
//
// Placed as a PEER of the presets (like `testPropOverride`), not a top-level `rules` entry: a
// top-level entry does not outrank the vitest preset's own file-scoped override, so a repeal
// written there has no effect. The CI job `sacred-unwritable-by-tools` makes this repeal's effect
// checkable rather than assumed — it runs every fixer and requires the sacred lane byte-identical.
const assertionRewriteRepeal = {
  overrides: [
    {
      files: ["**/*.{test,spec}.{ts,tsx,js,jsx}"],
      plugins: ["vitest"],
      rules: {
        "vitest/prefer-to-be-falsy": "off",
        "vitest/prefer-to-be-truthy": "off",
      },
    },
  ],
};

// Ultracite 7.9.4 moved the GitHub and SonarJS rules from core into an opt-in JS-plugin preset.
// Keep them; drop React Doctor, which this toolchain does not install.
const githubAndSonar = {
  ...jsPlugins,
  jsPlugins: jsPlugins.jsPlugins?.filter(
    (plugin) => typeof plugin === "string" || plugin.name !== "react-doctor"
  ),
  rules: Object.fromEntries(
    Object.entries(jsPlugins.rules ?? {}).filter(
      ([rule]) => !rule.startsWith("react-doctor/")
    )
  ),
};

// oxlint-disable-next-line typescript/no-unsafe-type-assertion -- sanctioned toolchain bridge
const ultraciteRules = [
  core,
  githubAndSonar,
  vitest,
  testPropOverride,
  conformanceOverride,
  assertionRewriteRepeal,
] as unknown as OxlintConfig[];

// The Bun smoke suite imports `bun:test`, which the Node-side oxlint/tsgolint
// type-aware pass cannot resolve (it is intentionally excluded from the tsc
// project). Bun runs and type-checks it on its own runtime.
const BUN_SMOKE_GLOB = "**/test/bun-smoke/**";
const NODE_MODULES_GLOB = "**/node_modules/**";

// fast-check's global seed/verbosity setup, owned by the harness and shared by every
// fast-check project here. Absolute, because the harness project sets its own `root`.
const FC_SETUP = fileURLToPath(
  new URL("../cache-harness/src/fc-setup.ts", import.meta.url)
);

// The workerd integration lane imports runtime-only `cloudflare:workers` /
// `cloudflare:test` modules, which the Node-side tsgolint pass resolves against each
// file's nearest tsconfig (the package/Node one) and cannot see — the same carve-out
// reason as the Bun smoke suite. It is typechecked instead under
// test/integration/tsconfig.json (the Workers-typed program).
//
// the `src/adapters/cloudflare/**` platform boundary is NO LONGER
// blanket-excluded — the full NON-type-aware oxlint rule set runs over it (syntax-level
// rules need no `cloudflare:` resolution). tsgolint's TYPE-AWARE pass naturally skips it
// (the adapter is excluded from the Node tsc program, so it is in no type-checked
// project — type-aware rules simply don't apply, no error); the Workers-typed program
// (integration tsconfig) is the adapter's type gate, run direct in CI.
const INTEGRATION_GLOB = "**/test/integration/**";

// The module-scope boot witness (test/boot). Same carve-out reason as the
// integration lane: it is a Worker that imports `cloudflare:workers`, which the Node-side type-aware
// pass cannot resolve. It is typechecked under the integration tsconfig (the Workers-typed program,
// run direct in CI) and RUN by `scripts/module-scope-boot.mjs` on `wrangler dev` — the only rig here
// whose module scope is the isolate's global scope.
const BOOT_GLOB = "**/test/boot/**";

// The RR-v8 build-smoke fixture (fixtures/cache-react-router). Same carve-out reason as the bun/
// integration lanes: it imports runtime-only virtuals (`virtual:react-router/server-build`) and
// builds under REAL Vite 8 with JSX/react — a code shape the kernel's Node-side type-aware lint
// program cannot resolve. Its gate is the `build-smoke` CI job (`react-router build`), not oxlint.

// `apps/**` is NO LONGER blanket-excluded. The old exclusion took the whole apps tree, and its
// premise was the fixture's alone — a minimal thing whose gate is that it BUILDS. The demo is a
// deliverable: it has a test lane, a narration that is its actual argument, and two Proxies over
// platform objects, which is precisely the code that fails in ways review does not catch. Leaving it
// unlinted because it happened to live under the same directory as the fixture was an accident of
// globbing, and it cost real findings — an unused import, a nested-ternary thicket in the narration,
// and two `any` returns leaking out of `Reflect.apply` in the wrappers, all of which the linter found
// the moment it was allowed to look.
//
// WHAT STAYS CARVED OUT: the demo's Workers ENTRY (`apps/cache-demo/workers/**`) — under the RE-MEASURED
// premise below, because the premise it replaces was false in its load-bearing sentence.
//
// The entry imports `cloudflare:workers` and `virtual:react-router/server-build` — runtime-only
// specifiers the Node-side type-aware pass cannot resolve. That much held up under re-measurement.
// An unresolvable import does not fail honestly: it makes `env` an `error` type and then reports
// findings ABOUT ITS OWN FAILURE TO RESOLVE. Its type gate is `apps/cache-demo/tsconfig.workers.json` (the
// Workers-typed program, run in the demo lane), exactly as the integration lane's entry is gated by
// its own Workers-typed program rather than by oxlint.
//
// WHAT WAS FALSE — and it is the sentence a carve-out lives or dies by. This comment used to claim
// that "every single diagnostic it produced was a cascade from two unresolved specifiers". Re-run at
// full resolution, the entry produced FIFTEEN diagnostics: thirteen cascades, and TWO that were
// nothing of the kind — `promise-function-async` on the build thunk and on `fetch`, both real, both
// independent of any specifier resolving. The exclusion had been hiding them behind a measurement
// nobody re-ran. They are FIXED in the entry rather than suppressed: a finding you can fix is not a
// finding you carve out. The census now stands at fourteen, all of them cascades (the two TS2307s,
// an `import.meta.env` the orphaned file's program cannot type, two names that live in
// `worker-configuration.d.ts`, and nine `no-unsafe-*` reported literally against an `error` typed
// value or an `any` the unresolvable import produced). Those fourteen are TS diagnostics, which have
// no point-of-use suppression to give them — that is what makes the glob the honest instrument here
// rather than merely the convenient one. Re-measure before trusting this paragraph; its predecessor
// was trusted for exactly as long as nobody did.
const DEMO_WORKER_GLOB = "workers/**";

// THE DEMO'S BUILD CONFIGS ARE NOT CARVED OUT, and the exclusion that used to take them is deleted.
// Its stated grounds were borrowed from the entry above — that they import `cloudflare:workers` /
// `virtual:react-router/server-build`. Measured: `vite.config.ts` imports @cloudflare/vite-plugin,
// @react-router/dev/vite and vite; `react-router.config.ts` imports one type from
// @react-router/dev/config. NEITHER imports either specifier; both sit inside
// `apps/cache-demo/tsconfig.json`'s own `include`, so both resolve; and both lint AND format clean with
// the exclusion gone. A suppression's premise is verified by measurement, not inherited from the
// file it happens to sit next to.

const ignorePatterns = [
  ...(core.ignorePatterns ?? []),
  BUN_SMOKE_GLOB,
  INTEGRATION_GLOB,
  BOOT_GLOB,
  DEMO_WORKER_GLOB,
  // The `wrangler types` artifact: generated, committed, and drift-guarded by the demo lane.
  "worker-configuration.d.ts",
  // Commit-zero files (born-clean repo): leave README as authored.
  "README.md",
  // The harness and demo carry two-line oxfmt/oxlint configs that re-export this policy; their own
  // tsconfigs cannot import a `.ts` path, and there is nothing in them to lint.
  "oxfmt.config.ts",
  "oxlint.config.ts",
  // CI helper scripts are plain untyped ESM (they walk a parsed-JSON report); the
  // strict TS-oriented lint is for library + test code, not build tooling.
  "scripts/**",
];

export default defineConfig({
  // Per-package pack config (bundler = vp pack / tsdown). Emits ESM `.js` +
  // `.d.ts` (not tsdown's default `.mjs`/`.d.mts`) so the publishConfig exports map
  // resolves and publint --strict / attw --profile esm-only validate a real build (P4).
  // Nothing is npm-published this phase; the only published build is CI's `lint:publish`, a
  // throwaway dist that publint --strict / attw validate. Phase 2 SHIPS the `.` + `./cloudflare`
  // surfaces (this file's two entries) — the artifact is the proof, so the gate builds it for real.
  //
  // THREE published entries: the kernel `.` (→ dist/index.*), the Cloudflare adapter
  // `./cloudflare` (→ dist/adapters/cloudflare/index.*), AND the React-Router adapter
  // `./react-router` (→ dist/adapters/react-router/index.*). ONE config with ALL entries so
  // rolldown hoists the shared kernel into a common chunk — each adapter re-imports it rather
  // than inlining a second copy.
  //
  // THIS IS SEMANTIC, NOT SIZE HYGIENE — and stated GENERICALLY, because the specific instance this
  // comment used to name (the replication-poller's WeakMap) was only ever an example of it, and naming
  // the example is how a comment goes stale while the property it guards grows. THE PROPERTY: the
  // kernel keeps module-level state in module-private WeakMaps, and a chunk IS a module instance —
  // fork the chunk and you fork every one of those maps. One kernel chunk means ONE module instance
  // behind every door.
  //
  // AND THE PROPERTY IS NOW MORE LOAD-BEARING, NOT LESS. The identity-alias fix funnels EVERY
  // instance-keyed kernel seam through a single map — `identityAliases` in `src/cache.ts`, which
  // `resolveCacheIdentity` consults before any seam keys anything — so that one map is what lets a
  // facade and the instance it fronts address the same slot. Concentrating the seams there was the
  // right fix, and it also means one forked chunk now severs all of them at once.
  //
  // FORK IT AND THE FAILURE IS SILENT, AND IT IS A PRIVACY FAILURE. `createWorkersCache` — reached
  // through the `./cloudflare` door — would register its facade alias in one copy of `identityAliases`,
  // while the RR middleware, reached through `./react-router`, binds and resolves against a copy that
  // has never heard of that facade. The render's dependencies are then recorded under a key nobody
  // reads: the collector comes back empty, `managed === 0`, and the header decision takes its
  // jurisdiction-floor branch — so an app's `public, s-maxage` ships on a per-principal body. That is
  // exactly the leak, re-opened by a build setting. Every error-class `instanceof` across doors
  // breaks the same way, for the same reason.
  //
  // So do NOT "optimize" the chunks apart. This is not left to be remembered: the publish assertion
  // (`scripts/assert-no-async-hooks.mjs`, run by `lint:publish`) checks on the BUILT dist that both
  // adapter doors import the SAME kernel chunk file. The hoisting holds today — measured, both doors
  // import one shared chunk — and that check is what keeps someone looking. `cloudflare:*`
  // runtime modules are external (workerd provides them at runtime), and `react`/`react-router` are
  // external peer deps (never bundled into the published `.js`). `tsconfig.pack.json` is the only
  // program that spans every subpath's type world (Node `node:crypto` for the kernel + the workerd
  // runtime types + `cloudflare:workers` for the CF adapter + `react-router`/node:async_hooks for RR).
  pack: {
    clean: true,
    dts: true,
    deps: { neverBundle: [/^cloudflare:/u, /^react$/u, /^react-router$/u] },
    entry: {
      "adapters/cloudflare/index": "src/adapters/cloudflare/index.ts",
      "adapters/react-router/index": "src/adapters/react-router/index.ts",
      index: "src/index.ts",
    },
    format: "esm",
    outExtensions: () => ({ dts: ".d.ts", js: ".js" }),
    sourcemap: false,
    tsconfig: "tsconfig.pack.json",
  },
  fmt: {
    ...ultraciteFmt,
    ignorePatterns,
  },
  lint: {
    extends: ultraciteRules,
    ignorePatterns,
    options: {
      typeAware: true,
      typeCheck: true,
    },
    // Curated overrides. The ultracite core preset is calibrated for astilba's
    // browser + React app monorepo; this repo is a headless correctness kernel and
    // a deterministic simulation harness — a different code shape (driver contracts
    // as methods, a seeded PRNG, throwing type-stubs, promise-plumbing fakes, and
    // citation-dense sacred tests). The rules below are pure style / false-positives
    // for THAT shape; every correctness and safety rule (no-explicit-any,
    // no-floating-promises, no-unsafe-*, exhaustive switch, …) stays ON.
    rules: {
      // Public driver/cache contracts read naturally as method signatures.
      "typescript/method-signature-style": "off",
      // Constructor parameter properties are idiomatic TS for the fakes.
      "typescript/parameter-properties": "off",
      // Exported factory functions (`t`, `compound`, `duration`, `world`, …) are
      // clearer as declarations than as `const x = () => …`.
      "func-style": "off",
      // `i++` in counters/loops is clear and intended.
      "no-plusplus": "off",
      // This repo REQUIRES inline spec citations / provenance.
      "no-inline-comments": "off",
      // The seeded PRNG (SimRng) is defined by its bitwise operations.
      "no-bitwise": "off",
      // Sim/fake methods cohere on their class even when a given one omits `this`.
      "class-methods-use-this": "off",
      // Small related fakes/error types are grouped one concept per file.
      "max-classes-per-file": "off",
      // Throwing type-stubs and promise-returning delegators are async-shaped
      // without an `await`; the async signature is the contract, not the body.
      "require-await": "off",
      // Ordered time advancement in a few traces awaits sequentially by design.
      "no-await-in-loop": "off",
      // The harness deliberately wraps sync fake logic in `Promise.resolve().then`
      // so injected failures surface as rejections (a Node-runtime concern; the
      // github plugin is browser-oriented).
      "github/no-then": "off",
      // `import fc from "fast-check"; fc.scheduler(…)` is fast-check's own API shape.
      "import/no-named-as-default-member": "off",
      // Barrel re-exports (`export * from …`) are the harness's public index.
      "sonarjs/no-wildcard-import": "off",
      // Object keys are ordered by meaning (and interleaved with conditional
      // spreads that alphabetical ordering cannot express).
      "sort-keys": "off",
      // The seeded PRNG's uint32 coercion (`>>> 0`) is bitwise, not truncation.
      "unicorn/prefer-math-trunc": "off",
      // The sacred-test-discipline headers reference vitest's `.todo`/`.skip`
      // APIs; that is not a work-marker. The repo carries no real TODO markers.
      "sonarjs/todo-tag": "off",
      // Both `index.ts` barrels are the intended single-import public surfaces
      // (the harness one is imported only by tests); the module-count heuristic is
      // not a correctness signal here.
      "oxc/no-barrel-file": "off",
      // `@testExport` is knip's point-of-use suppression tag (knip.json `tags: ["-testExport"]`)
      // marking an adapter export that exists SOLELY for a test to assert the single source of truth
      // — no production importer, so `knip --production` would false-flag it as unused (the adapter is
      // now a PUBLISHED subgraph, C4; the exclusion that formerly hid these is gone). It is a tool
      // marker, not a doc tag, so it is declared VALID rather than suppressing the tag-name check.
      // (Distinct from `@offGraph`, retired: THIS premise is live — the six tagged exports
      // are genuinely test-only internals, ruled point-of-use-tagged rather than made public.)
      "jsdoc/check-tag-names": ["error", { definedTags: ["testExport"] }],
    },
  },
  test: {
    // `harness` holds the harness self-tests (non-vacuous foundation — a broken fake would
    // silently invalidate every invariant above it); `unit` holds this package's unit teeth; `invariants`
    // holds the 29 invariants + the red-team-five scenarios (the sole semantics
    // authority, fakes-only); `conformance` holds the phase-2 parameterized driver
    // contracts (deterministic lane) run against the v0.1 fakes as reference impls;
    // `integration` (phase-2) runs the workerd lane via
    // @cloudflare/vitest-pool-workers and lives in its OWN config file (vitest 4 wires
    // the pool through the `cloudflareTest` plugin, kept clear of this vp-vite config).
    // The Node projects exclude the Bun smoke suite so Node-side vitest never resolves
    // `bun:test`; the integration project runs off workerd, scoped to test/integration/.
    projects: [
      {
        test: {
          environment: "node",
          exclude: [BUN_SMOKE_GLOB, NODE_MODULES_GLOB],
          include: ["src/**/*.test.ts"],
          name: "harness",
          root: "../cache-harness",
          setupFiles: [FC_SETUP],
        },
      },
      {
        test: {
          environment: "node",
          exclude: [BUN_SMOKE_GLOB, NODE_MODULES_GLOB],
          include: ["test/unit/**/*.test.ts"],
          name: "unit",
          setupFiles: [FC_SETUP],
        },
      },
      {
        test: {
          environment: "node",
          exclude: [BUN_SMOKE_GLOB, NODE_MODULES_GLOB],
          include: ["test/{invariants,scenarios}/**/*.test.ts"],
          name: "invariants",
          setupFiles: [FC_SETUP],
        },
      },
      {
        test: {
          // Conformance is deterministic (lane law): parameterized driver contracts
          // driven by explicit cases, no fast-check PRNG — so no fc-setup here.
          environment: "node",
          exclude: [BUN_SMOKE_GLOB, NODE_MODULES_GLOB],
          include: ["test/conformance/**/*.test.ts"],
          name: "conformance",
        },
      },
      {
        test: {
          // The chaos demo's OWN machinery (apps/cache-demo): the binding wrappers toggle, the
          // scenes arm what they claim, the narration renders and refuses to assert an SLO. It is
          // NOT a fifth lane in the lane-law sense — it proves the DEMO, never the library, and it
          // deliberately re-tests no kernel semantics (that would be duplicate coverage of the
          // sacred suite's own subject, with worse determinism). Deterministic and pure: the
          // wrappers are driven against plain stand-ins, so no Workers runtime and no timing
          // tolerance — the demo's real-binding proof is its build + the scenes running under
          // Miniflare, not this.
          environment: "node",
          exclude: [BUN_SMOKE_GLOB, NODE_MODULES_GLOB],
          include: ["**/*.test.ts"],
          name: "demo",
          root: "../../apps/cache-demo",
        },
      },
      // The workerd integration lane. Referenced by path (not inlined): its config
      // imports the pool-workers `cloudflareTest` plugin against plain vitest/config,
      // so no plugin-type skew touches this vp-bundled-vite root config.
      "./test/integration/vitest.config.ts",
    ],
    coverage: {
      // istanbul, not the v8 default: v8 coverage can't run on Bun or Workers,
      // both kernel targets. No `src` threshold gate while the kernel is
      // unimplemented (~0%, everything throws) — thresholds arrive at green (P-A2).
      provider: "istanbul",
      reporter: ["text"],
    },
  },
});
