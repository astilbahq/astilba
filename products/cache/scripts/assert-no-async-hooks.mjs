// Build assertions over the packed dist. TWO properties, both about the SHAPE of the built import
// graph, which is why they share this script's one apparatus (a walk of the dist's own edges):
//
//   1. `node:async_hooks` may be imported by the `./react-router` door and by NOTHING ELSE the other
//      doors can reach (C4-c) — the original subject, documented at length below.
//   2. Both adapter doors import the SAME kernel chunk (E28/E23) — one module instance behind every
//      door, so the kernel's instance-keyed WeakMap seams are one map and not three. Its rationale
//      sits with the assertion itself, further down.
//
// WHY IT IS A GATE AND NOT A COMMENT. A Workers deployment without the `nodejs_als` compat flag
// throws on a static `node:async_hooks` import at IMPORT time — before a single line of user code
// runs. So a `./cloudflare` (or `.`) consumer who never asked for render collection would not get a
// degraded feature; they would get a Worker that cannot boot.
//
// THAT HARM IS CURRENTLY MASKED — WHICH IS NOT THE SAME AS ABSENT, AND IS WHY THE GATE STAYS.
// `packages/cache/src/cache.ts` statically imports `node:crypto` (createHash/createHmac, used at
// runtime), and it sits in the one shared kernel chunk every door pulls in. So a `.` or
// `./cloudflare` consumer already needs `nodejs_compat` to boot AT ALL — and `nodejs_compat`
// includes ALS. Via the compat-flag path, then, the boot failure this gate names cannot presently
// occur: the flag that makes the kernel importable already carries the flag the ALS needs.
//
// STATE THE MASKING PRECISELY, because the obvious near-miss is wrong in an instructive way. Across
// the compat-flag space there is NO setting that boots this kernel and rejects async_hooks:
// `nodejs_compat` permits both crypto and the ALS, and `nodejs_als` alone cannot boot the kernel at
// all (it supplies no `node:crypto`). Note what that rules out — a deployment setting just
// `nodejs_als` is not sunk by the async_hooks import, since `nodejs_als` is precisely the flag that
// makes that import legal (see the second paragraph above). So the harm cannot occur today for any
// consumer who can boot in the first place. The gate stands on the two things that are true anyway:
//   • THE STANDING RATIONALE — it evaporates the instant the kernel drops `node:crypto`. A WebCrypto
//     migration is ledgered for precisely that, and on the day it lands the kernel boots with no flag
//     at all, `nodejs_als` becomes a flag a consumer has every reason not to set, and the harm above
//     is live again with nothing between it and a consumer. A gate that only matters after a future
//     refactor has to be standing BEFORE it, because the refactor is when nobody is thinking about
//     async_hooks;
//   • THE RESIDUAL CASE, which runs OUTSIDE the flag space entirely: a consumer who supplies
//     `node:crypto` through their own bundler (alias or polyfill) and sets NEITHER flag. The kernel
//     imports and boots — their crypto never needed `nodejs_compat` — and an async_hooks import in a
//     shared chunk throws, because `nodejs_als` is the one thing that would have permitted it and
//     they had no reason to set it.
//
// So the rationale for the kernel reaching the render collector through an in-package WeakMap seam
// (rather than importing the ALS itself) stands, and a rationale with no gate under it is one
// refactor from being undone by someone who never heard it.
//
// WHAT IT CHECKS. Reachability through the built dist's import graph, not a flat grep of the files:
// the doors share a hoisted kernel chunk (see packages/cache/vite.config.ts), so what can sink a
// Worker is anything the door pulls in TRANSITIVELY. From each forbidden door we walk every
// relative import and fail if any file reached that way imports `async_hooks`.
//
// WHY NOT A BARE STRING GREP. The shared kernel chunk legitimately CONTAINS the string
// `async_hooks` — in the doc comment explaining why it must never import it. A comment cannot crash
// a Worker; an import can. So the check matches an import SPECIFIER (static, dynamic, or require),
// which is the thing with teeth.
//
// NON-VACUITY. The `./react-router` door must actually reach an `async_hooks` import — that is where
// the ALS legitimately lives. If it does not, either the build's shape changed or the matcher below
// stopped matching, and in both cases this script would be silently passing everything. So that is
// a failure too: a guard that cannot see the one import it knows exists is not a guard.

import { readFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const DIST = resolve(dirname(fileURLToPath(import.meta.url)), "../packages/cache/dist");

/** An `async_hooks` import in any form the bundler can emit: `from "node:async_hooks"`,
 * `import "async_hooks"`, `import("node:async_hooks")`, `require("async_hooks")`. The quote is what
 * makes it an import specifier rather than prose — the kernel chunk's comment names the module in
 * backticks, and that is exactly the difference this pattern turns on. */
const ASYNC_HOOKS_IMPORT =
  /(?:\bfrom|\bimport|\brequire)\s*\(?\s*(["'])(?:node:)?async_hooks\1/u;

/** Every relative specifier a chunk imports (the edges of the dist's own graph — bare specifiers
 * are externals: `react-router`, `cloudflare:workers`, `node:crypto`). */
const RELATIVE_IMPORT = /(?:\bfrom|\bimport)\s*\(?\s*["'](\.[^"']*)["']/gu;

/** The doors, as the package's `exports` map publishes them. */
const DOORS = {
  ".": "index.js",
  "./cloudflare": "adapters/cloudflare/index.js",
  "./react-router": "adapters/react-router/index.js",
};

/** The doors that must not be able to reach the ALS, at any depth. `./react-router` is absent by
 * design: it is where the ALS lives, and an RR app is one that already opted into it. */
const FORBIDDEN_DOORS = [".", "./cloudflare"];

/** Every file reachable from `entry` through relative imports, `entry` included. */
function reachableFrom(entry) {
  const seen = new Set();
  const queue = [resolve(DIST, entry)];
  while (queue.length > 0) {
    const file = queue.pop();
    if (seen.has(file)) {
      continue;
    }
    seen.add(file);
    const source = readFileSync(file, "utf8");
    for (const [, specifier] of source.matchAll(RELATIVE_IMPORT)) {
      queue.push(resolve(dirname(file), specifier));
    }
  }
  return seen;
}

/** The files in `entry`'s import graph that import `async_hooks`, as dist-relative paths. */
function alsImportersUnder(entry) {
  return [...reachableFrom(entry)]
    .filter((file) => ASYNC_HOOKS_IMPORT.test(readFileSync(file, "utf8")))
    .map((file) => relative(DIST, file))
    .sort();
}

/** Every dist file the `.` door is built from, minus the door module itself — i.e. the shared kernel
 * chunk(s) rolldown hoisted out of the three entries. */
function kernelChunks() {
  const entry = resolve(DIST, DOORS["."]);
  return [...reachableFrom(DOORS["."])].filter((file) => file !== entry);
}

const failures = [];

// ── ONE KERNEL CHUNK BEHIND EVERY DOOR (packages/cache/vite.config.ts) ──────────────────────────
// A chunk is a module instance, so a forked chunk forks the kernel's module-private WeakMaps. Every
// instance-keyed seam resolves through ONE of them (`identityAliases`, via `resolveCacheIdentity`),
// which means a fork severs the lot at once — and silently: the `./cloudflare` door would register a
// facade alias in one copy while the `./react-router` door resolves against another, the render's
// dependencies land under a key nobody reads, `managed === 0`, and an app's `public, s-maxage` ships
// on a per-principal body. That is the E23 leak, re-opened by a build setting rather than by code,
// which is why the assertion is here — on the BUILT artifact — and not in a test.
//
// The hoisting holds today. This is what makes looking permanent, since nothing else would notice.
const KERNEL = kernelChunks();
if (KERNEL.length === 0) {
  // NON-VACUITY: with no shared chunk there is nothing to compare doors against, and the loop below
  // would pass by having no work. That state is itself the defect (the kernel inlined into the
  // barrel), so it is reported rather than skipped.
  failures.push(
    'the "." door is built from no shared chunk at all — the kernel is inlined into its own barrel, so each door almost certainly carries a private copy and this assertion has nothing left to check'
  );
}
for (const door of ["./cloudflare", "./react-router"]) {
  const reach = reachableFrom(DOORS[door]);
  const forked = KERNEL.filter((chunk) => !reach.has(chunk))
    .map((file) => relative(DIST, file))
    .sort();
  if (forked.length > 0) {
    failures.push(
      `the "${door}" door does not import the kernel chunk the "." door is built from (${forked.join(", ")}) — it carries its own copy, so its module-level WeakMaps are a different instance from the other doors'. The identity alias a facade registers through one door is then invisible through the other: the render collector reads empty, managed === 0, and an app-set "public, s-maxage" survives on a per-principal body (E23). Do not split the kernel chunk apart.`
    );
  }
}

for (const door of FORBIDDEN_DOORS) {
  const importers = alsImportersUnder(DOORS[door]);
  if (importers.length > 0) {
    failures.push(
      `the "${door}" door reaches an async_hooks import via: ${importers.join(", ")} — a Worker without the nodejs_als compat flag would throw at import time`
    );
  }
}

// The self-test: the door that IS allowed to import it had better be importing it.
if (alsImportersUnder(DOORS["./react-router"]).length === 0) {
  failures.push(
    'the "./react-router" door imports no async_hooks — the build shape or this matcher changed, so this guard is asserting nothing'
  );
}

if (failures.length > 0) {
  for (const failure of failures) {
    console.error(`::error::${failure}`);
  }
  process.exit(1);
}

console.log(
  `assert-no-async-hooks: the ${FORBIDDEN_DOORS.map((door) => `"${door}"`).join(" and ")} doors reach no async_hooks import (checked ${join(DIST, "**")}); "./react-router" does, as it must. Both adapter doors import the one kernel chunk (${KERNEL.map((file) => relative(DIST, file)).join(", ")}).`
);
