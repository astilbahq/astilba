// Boot assertion: the RR fixture's Worker STARTS on workerd and serves a request.
//
// WHY THIS IS A SEPARATE GATE FROM build-smoke. A build proves the TOOLCHAIN — that real Vite 8
// compiles the RR adapter and the kernel. It cannot prove the RUNTIME PROFILE, and not by omission:
// a Workers build leaves `node:` specifiers as UNRESOLVED EXTERNALS. `build/server/assets/
// server-build-*.js` ships the literal strings "node:crypto" and "node:async_hooks", and whether the
// isolate can resolve them is decided entirely by the deployment's compat flags — a question no
// build asks. So the two gates answer different questions and neither substitutes for the other:
// build-proves-toolchain, boot-proves-runtime-profile.
//
// WHAT IT PROVES, AND NOTHING MORE. `@astilba/cache` statically imports `node:crypto` in the one
// shared chunk every door pulls in, and the RR middleware imports `node:async_hooks`. Under a wrong
// flag the Worker cannot IMPORT the library — it never boots. This gate starts workerd against the
// BUILT output and issues ONE request. No bindings, no drivers, no cloud credentials: flag + crypto,
// and nothing else.
//
// WHAT THE ASSERTION IS, AND WHY IT IS NOT A 200. The fixture composes a DRIVERLESS cache (no L2 —
// deliberately: a fixture that grew bindings would stop being a minimal boot witness), and
// `getOrSet` without an `l2` throws `NotImplementedError` in this phase. So the loader throws, RR
// renders its error boundary, and the response is a 500 — under BOTH a right and a wrong flag. The
// status therefore discriminates nothing, and a gate asserting one would be asserting the fixture's
// phase rather than its boot. What DOES discriminate is measured, not assumed:
//
//     nodejs_als     →  body: `Error: No such module "node:crypto".`   (the module graph never loaded)
//     nodejs_compat  →  body: `<!DOCTYPE html>…`                       (the app rendered)
//
// A rendered document is only reachable once the server build IMPORTED — which is exactly the fact
// this gate exists to hold: the isolate resolved `node:crypto`, the middleware's ALS frame opened,
// and the kernel ran (far enough to reach its own phase-honest `NotImplementedError`). That is the
// boot. So the assertion is POSITIVE — the app rendered — never merely "no error seen", which is how
// a gate learns to pass on silence.
//
// TIGHTEN THIS when `getOrSet` no longer needs an `l2`, or when the fixture legitimately gains one:
// the request becomes a 200 and the boot proof gets stronger for free. Until then a 200 is not
// available here without giving the fixture drivers, which would cost more than it proves.

import { spawn } from "node:child_process";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const FIXTURE = join(ROOT, "../../fixtures/cache-react-router");
/** The wrangler config the BUILD emits (flags carried through from the fixture's own wrangler.jsonc). */
const BUILT_CONFIG = join(FIXTURE, "build/server/wrangler.json");
const PORT = 8788;
const URL_UNDER_TEST = `http://localhost:${PORT}/`;
/** workerd cold-start budget. Generous: this lane is allowed to be slow, never flaky. */
const BOOT_TIMEOUT_MS = 90_000;
const POLL_INTERVAL_MS = 500;

/** The signature of the failure this gate exists to catch — a module the isolate could not resolve. */
const UNRESOLVED_MODULE = "No such module";

const failures = [];
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const child = spawn(
  "pnpm",
  ["exec", "wrangler", "dev", "-c", BUILT_CONFIG, "--port", String(PORT)],
  {
    cwd: FIXTURE,
    // Never let wrangler phone home from CI, and never let it try to be interactive.
    env: { ...process.env, CI: "1", WRANGLER_SEND_METRICS: "false" },
    stdio: ["ignore", "pipe", "pipe"],
  }
);

let log = "";
child.stdout.on("data", (chunk) => {
  log += chunk;
});
child.stderr.on("data", (chunk) => {
  log += chunk;
});

/** Poll until the server answers. A dead child is reported as itself, never as a timeout. */
async function awaitResponse() {
  const deadline = Date.now() + BOOT_TIMEOUT_MS;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) {
      return { error: `wrangler exited early with code ${child.exitCode}` };
    }
    try {
      const response = await fetch(URL_UNDER_TEST);
      return { body: await response.text(), status: response.status };
    } catch {
      // Not listening yet — the only expected reason a fetch fails during cold start.
      await sleep(POLL_INTERVAL_MS);
    }
  }
  return { error: `no response within ${BOOT_TIMEOUT_MS}ms` };
}

const result = await awaitResponse();
child.kill("SIGTERM");

if (result.error) {
  failures.push(
    `${result.error} — the Worker did not serve a request. Last output:\n${log}`
  );
} else {
  if (result.body.includes(UNRESOLVED_MODULE)) {
    failures.push(
      `the isolate could not resolve a module the library imports — the Worker cannot boot under this deployment's compat flags. Response (${result.status}): ${result.body.slice(0, 300)}`
    );
  }
  // The POSITIVE proof: a rendered document is unreachable unless the server build imported, which
  // is unreachable unless the isolate resolved `node:crypto` and `node:async_hooks`.
  if (!/^\s*<!DOCTYPE html>/iu.test(result.body)) {
    failures.push(
      `the Worker answered (${result.status}) but did not render the app — the server build did not import, so this proves no boot. Body: ${result.body.slice(0, 300)}`
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
  `boot-smoke: the fixture's Worker booted on workerd and served ${URL_UNDER_TEST} — the isolate resolved node:crypto + node:async_hooks and the app rendered (compat flags are correct).`
);
