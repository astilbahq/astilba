// The typed-routes DIRTY-TREE CHECK — proof that apps/demo's generated route types are
// generated, load-bearing, and correctly wired.
//
// WHAT THIS EXISTS TO CATCH, and why a green typecheck does not catch it. `react-router typegen`
// writes a `.react-router/types` overlay: the `Route` namespaces each route imports its own arg
// types from, the `Register` interface that makes `href("/scene/:sceneId", …)` a checked call, and
// the declaration for the virtual server-build module the Worker entry imports. `rootDirs` merges
// that overlay with the app's own tree so `./+types/home` resolves.
//
// Two ways that arrangement rots, both silent, and a passing `tsc` is consistent with both:
//
//   • THE DEAD OVERLAY. The overlay stops being consumed — an exclude, a widened type, a route that
//     quietly went back to hand-written args — and "typed routes" becomes decoration. Everything is
//     green. The types are generated on every install and read by nobody.
//   • THE HAND-MAINTAINED OVERLAY. Someone commits the generated files to make a red tree green.
//     Now they are a snapshot that drifts from `routes.ts` and is still green on the day it lies.
//
// So this asserts BOTH DIRECTIONS, which is the whole trick: without the overlay the programs must
// FAIL, and with it they must PASS. The first half is what makes the types load-bearing — it is not
// possible to satisfy it with an overlay nobody reads. The second half is what proves `rootDirs` is
// wired rather than the overlay merely being absent-and-ignored (the trap the build-smoke fixture
// next door sidesteps by omitting `+types` entirely, which is the right call for a fixture whose
// value is a minimal surface).
//
// A NOTE ON THE FIRST HALF'S HONESTY: "tsc fails" is a weak assertion on its own — tsc fails for a
// thousand reasons, and a check satisfied by ANY error is a check that would pass on a typo. So the
// failure is required to name the missing overlay (an unresolved `./+types/…` import, or the
// virtual server-build module), not merely to be a failure.

import { execFileSync } from "node:child_process";
import { existsSync, rmSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const DEMO = resolve(dirname(fileURLToPath(import.meta.url)), "../apps/demo");
const OVERLAY = join(DEMO, ".react-router");

// The compiler, resolved and invoked DIRECTLY (`node <tsc>`), never through `pnpm exec`. `pnpm exec`
// can run the demo's `prepare` script (`react-router typegen`) as a dependency-sync side effect, and
// that SILENTLY REGENERATES the overlay Direction 1 has just deleted — so tsc compiles green and the
// check passes vacuously, reporting the overlay dead. It is nondeterministic (whether pnpm decides a
// sync is due), so it does not fail honestly; it flakes. Measured directly after the react-router
// 8.3.0 / toolchain bump: back-to-back runs of this script disagreed (one vacuous-green, one
// correct). A direct node invocation runs the same compiler with no package lifecycle, so a deleted
// overlay stays deleted and Direction 1 observes what it is there to observe.
const TSC = createRequire(import.meta.url).resolve("typescript/bin/tsc");

/** The two programs the overlay feeds: the app (route `+types`, the `Register` map) and the Worker
 * entry (the virtual server-build declaration). Both must depend on it. */
const PROGRAMS = [
  { args: ["--noEmit"], name: "app (tsconfig.json)" },
  {
    args: ["-p", "tsconfig.workers.json", "--noEmit"],
    name: "worker (tsconfig.workers.json)",
  },
];

/** The overlay's fingerprint in a compiler error: an unresolved `+types` import, or the virtual
 * server-build module the typegen declares. An error that is neither is a DIFFERENT breakage, and
 * this check must not accept it as its own proof. */
const OVERLAY_ABSENCE = /\+types\/|virtual:react-router\/server-build/u;

function tsc(program) {
  try {
    execFileSync(process.execPath, [TSC, ...program.args], {
      cwd: DEMO,
      encoding: "utf8",
      stdio: "pipe",
    });
    return { ok: true, output: "" };
  } catch (error) {
    return { ok: false, output: `${error.stdout ?? ""}${error.stderr ?? ""}` };
  }
}

function typegen() {
  execFileSync("pnpm", ["exec", "react-router", "typegen"], {
    cwd: DEMO,
    stdio: "pipe",
  });
}

const failures = [];

// ── Direction 1: WITHOUT the overlay, both programs must fail, and fail ABOUT the overlay ──
rmSync(OVERLAY, { force: true, recursive: true });
if (existsSync(OVERLAY)) {
  failures.push(`could not remove ${OVERLAY} — this check proved nothing`);
}
for (const program of PROGRAMS) {
  const result = tsc(program);
  if (result.ok) {
    failures.push(
      `${program.name} COMPILED with no generated route types. They are decoration: something reads them in principle and nothing depends on them in fact, which is the dead-overlay trap this check exists to prevent.`
    );
  } else if (!OVERLAY_ABSENCE.test(result.output)) {
    failures.push(
      `${program.name} failed without the route types, but not BECAUSE of them — no unresolved '+types/…' or virtual server-build import in its output. Something else is broken, and this check must not launder that into its own proof:\n${result.output.trim()}`
    );
  }
}

// ── Direction 2: WITH the overlay, both programs must pass ──
typegen();
for (const program of PROGRAMS) {
  const result = tsc(program);
  if (!result.ok) {
    failures.push(
      `${program.name} FAILED after react-router typegen — the generated types exist and the program still does not compile against them. Check 'rootDirs' in the tsconfig:\n${result.output.trim()}`
    );
  }
}

if (failures.length > 0) {
  for (const failure of failures) {
    console.error(`::error::${failure}`);
  }
  process.exit(1);
}

console.log(
  "demo-typed-routes: apps/demo's route types are GENERATED and LOAD-BEARING — both programs fail without them (naming them as the cause) and compile with them."
);
