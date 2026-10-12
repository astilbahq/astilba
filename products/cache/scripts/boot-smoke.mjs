



































import { spawn } from "node:child_process";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const FIXTURE = join(ROOT, "apps/rr-fixture");
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
