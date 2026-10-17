// THE MODULE-SCOPE BOOT WITNESS — one of three conformance instruments.
//
// WHAT THIS INSTRUMENT PROVES, AND ONLY THIS: THE PLACEMENT IS LEGAL. A Worker whose module scope
// builds the published factory starts on workerd, and the cache that construction produced goes on
// to answer requests. That is the entire finding. Its two neighbours hold the other two clauses —
// whether construction is COLD is the census's to rule on
// (`packages/cache/test/integration/construction-io-census.test.ts`), and whether handles are minted
// PER USE is the cross-request family's
// (`packages/cache/test/integration/cross-request.test.ts`). This file rules on neither. Each clause
// is held by the instrument that can actually see it, and this one speaks only to its own.
//
// WHY THE COLDNESS CLAIM IS NOT THIS FILE'S TO MAKE — and why an earlier version of this header made
// it anyway, wrongly. A pass here means the isolate started and served; it does NOT mean construction
// touched nothing. That gap was measured on this branch, by restoring each of the three
// construction-I/O sites in turn and running this gate against it:
//
//   • a SYNCHRONOUS mint throws during module evaluation, so workerd never starts the isolate. This
//     gate catches that one, and catches it totally.
//   • an ASYNC dial and a best-effort RPC do not throw anywhere this gate can look. Each fails as a
//     rejected promise that its own call site discards by design (a detached async call; a
//     try/catch that exists to swallow). The Worker boots, both requests serve, this banner prints
//     green — and construction performed I/O the whole time.
//
// So two of the three sites are invisible here, and they are invisible precisely because their
// failures are swallowed rather than fatal — which is also how they survived so long. A green banner
// is therefore evidence that construction did not THROW, and that is a different claim from cold.
// The census sees all three; that is why the clause is its and not this file's.
//
// WHY THIS IS A SEPARATE GATE FROM THE integration LANE, AND NOT A DUPLICATE OF IT. The two lanes
// disagree about what "module scope" is, and the disagreement is the whole reason this file exists.
// `@cloudflare/vitest-plugin` evaluates the `main` worker module lazily, inside the request
// context that first reaches for it — so under that pool a module-scope binding call SUCCEEDS, and a
// test placed there proves nothing about the placement the quickstart depicts. Measured, not
// assumed: the same construction that this gate catches at global scope runs clean under the pool.
// `wrangler dev` starts workerd and evaluates the entry when the ISOLATE starts, before any request
// exists — which is the deployed placement, and the only rig here that has it. One cache per isolate,
// built at module scope, is the shape the quickstart teaches, and this is what holds that shape
// runnable.
//
// WHAT THE SECOND REQUEST ACTUALLY SHOWS — written from what it does, not from what it was hoped to
// do. Both requests are COLD FILLS of DIFFERENT keys (`boot-1`, `boot-2`), so the second one is the
// first one again against a key that is equally absent. What that establishes is real but narrow:
// the module-scope instance is still usable across a request boundary — it did not wedge, and the
// isolate did not die with the request that first touched it.
//
// WHAT IT DOES NOT ESTABLISH, despite that same earlier header claiming it too: that connections are
// minted PER USE. That claim needs a request to reach a handle the PREVIOUS request minted, and two
// cold fills never do. Measured: a factory that memoizes its stub source — holding one
// connection forever, the exact shape the law forbids — walks past this gate green, and past the
// census as well. The cross-request family is where that liar is named and caught.
//
// NO CLOUD CREDENTIALS: Miniflare backs the KV namespace and the Durable Object locally.

import { spawn } from "node:child_process";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const FIXTURE = join(ROOT, "test/boot");
const CONFIG = join(FIXTURE, "wrangler.jsonc");
const PORT = 8788;
/** workerd cold-start budget. Generous: this lane is allowed to be slow, never flaky. */
const BOOT_TIMEOUT_MS = 90_000;
const POLL_INTERVAL_MS = 500;

/** The signature of the failure this gate exists to catch — I/O attempted at global scope. */
const GLOBAL_SCOPE_REFUSAL = "Disallowed operation called within global scope";
/** The signature of a client-held I/O handle reused across two request contexts. */
const CROSS_REQUEST_REFUSAL = "Cannot perform I/O on behalf of a different request";

const failures = [];
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const child = spawn(
  "pnpm",
  ["exec", "wrangler", "dev", "-c", CONFIG, "--port", String(PORT)],
  {
    // Run from the Cache package so pnpm resolves its own wrangler; the fixture directory is not a
    // package. -c is absolute, so wrangler still resolves the Worker relative to the fixture config.
    cwd: ROOT,
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
async function awaitResponse(url) {
  const deadline = Date.now() + BOOT_TIMEOUT_MS;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) {
      return { error: `wrangler exited early with code ${child.exitCode}` };
    }
    try {
      const response = await fetch(url);
      return { body: await response.text(), status: response.status };
    } catch {
      // Not listening yet — the only expected reason a fetch fails during cold start.
      await sleep(POLL_INTERVAL_MS);
    }
  }
  return { error: `no response within ${BOOT_TIMEOUT_MS}ms` };
}

/** Assert one request served. `label` names which of the two it was. */
function judge(label, result) {
  if (result.error) {
    failures.push(
      `${label}: ${result.error} — the Worker did not serve a request. Last output:\n${log}`
    );
    return;
  }
  // The two platform refusals this gate is built around, named individually so a failure says WHICH
  // law was broken rather than only that a body looked wrong.
  if (log.includes(GLOBAL_SCOPE_REFUSAL) || result.body.includes(GLOBAL_SCOPE_REFUSAL)) {
    failures.push(
      `${label}: construction performed I/O at global scope — workerd refused it and the isolate never started. A module-scope cache is the documented placement, so this is a broken quickstart, not a caller error.`
    );
    return;
  }
  if (result.body.includes(CROSS_REQUEST_REFUSAL)) {
    failures.push(
      `${label}: the instance reused an I/O handle minted in an earlier request. The platform ties a client-held handle to the request context that minted it and refuses it anywhere else, so a long-lived cache must hold ADDRESSES and mint per use. Body: ${result.body.slice(0, 300)}`
    );
    return;
  }
  // The POSITIVE proof: the factory ran and the value came back. Never merely "no error seen",
  // which is how a gate learns to pass on silence.
  if (!result.body.startsWith("OK ")) {
    failures.push(
      `${label}: the Worker answered (${result.status}) but did not serve the value. Body: ${result.body.slice(0, 400)}`
    );
  }
}

// Request 1 — the boot itself, and the first I/O the isolate ever performs.
judge("request 1 (boot + cold fill)", await awaitResponse(`http://localhost:${PORT}/?key=boot-1`));
// Request 2 — a SECOND request context against the SAME module-scope instance. Another COLD key, so
// this shows the instance still serves after a request boundary; it does not reach anything the
// first request minted (see this file's header, and the cross-request family for that clause).
judge("request 2 (the instance still serves after a request boundary)", await awaitResponse(`http://localhost:${PORT}/?key=boot-2`));

child.kill("SIGTERM");

if (failures.length > 0) {
  for (const failure of failures) {
    console.error(`::error::${failure}`);
  }
  process.exit(1);
}

console.log(
  "module-scope-boot: a Worker building the factory at module scope booted on workerd and served two requests off the one instance — the placement the quickstart teaches is legal, and the instance survived a request boundary. Whether construction was cold, and whether handles are minted per use, are not findings of this gate: see the census and the cross-request family."
);
