import { defineConfig } from "vite-plus";

// Per-package pack config (§1.1 bundler = vp pack / tsdown). Emits ESM `.js` +
// `.d.ts` (not tsdown's default `.mjs`/`.d.mts`) so the publishConfig exports map
// resolves and publint --strict / attw --profile esm-only validate a real build (P4).
// Nothing is npm-published this phase; the only published build is CI's `lint:publish`, a
// throwaway dist that publint --strict / attw validate. Phase 2 SHIPS the `.` + `./cloudflare`
// surfaces (this file's two entries) — the artifact is the proof, so the gate builds it for real.
//
// THREE published entries (E22 + C4-a-2): the kernel `.` (→ dist/index.*), the Cloudflare adapter
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
// exactly the E23 leak, re-opened by a build setting. Every error-class `instanceof` across doors
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
export default defineConfig({
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
});
