// THE DEMO BOOT WITNESS — the composed app, AS SHIPPED, establishes its bus channel on workerd.
//
// WHAT THIS INSTRUMENT PROVES, AND ONLY THIS. Boot `apps/demo` the way a deployment boots it — the
// output of `react-router build`, started on real workerd by `wrangler dev`, against the real
// Coordinator Durable Object and KV bindings — and the replication reader's bus channel reaches
// `established`: a `hello` completed over the Durable Object socket. That is the whole
// finding. The armed half of the run proves the same instrument can report the negative: with this
// demo's `bus-drop` fault armed, the channel is `never-established` and the library emits
// `bus_dial_failed`.
//
// WHAT IT DOES NOT PROVE. Not the bus MECHANISM's correctness — that is the do-bus integration
// lane's clause, and those tests hold it inside the test harness's own composition rather than the
// shipped app's. Different instrument, different clause: an app can compose a working mechanism
// wrongly, and a mechanism can be right in a harness that nothing ships. Not convergence TIMING —
// no number printed here is a latency claim, and a later measurement phase owns that clause. Not
// one line of kernel semantics: the sacred suite owns those and this lane never gets a vote on them.
// Naming an instrument without naming what it proves is how a green starts meaning nothing.
//
// WHY IT IS A LANE OF ITS OWN. The `demo` job proves the demo's own machinery — the binding wrappers
// interpose, each scene arms what it advertises, the narration renders — and that the app BUILDS.
// None of that RUNS the built app, so none of it can see a handshake. `boot-smoke` next door runs a
// built app on workerd, but a driverless one: its subject is the compat flags, and it binds nothing.
//
// THE GAUGE IS JSON, NEVER SCRAPED MARKUP. The channel is read from `/status.json`
// (`apps/demo/app/routes/status.ts`), which reports `explain().reader.channel` verbatim through the
// same witness call the console's channel row renders from. Parsing the rendered cell would work
// today and would break silently the first time the table's markup moved — a gauge that can go
// wrong without going red is worse than no gauge.
//
// BOTH ARMS RUN, AND THE ARMED ONE IS THIS LANE'S NEGATIVE CONTROL. A surface stuck on the string
// `established` would satisfy a positive-only probe vacuously, and nothing in that probe could tell
// it apart from a working handshake. So the armed arm is not a bonus scenario; it is the evidence
// that the healthy arm's green means anything. It requires `never-established` AND at least one
// `bus_dial_failed`, and the second condition is load-bearing: a merely NEWBORN reader also reads
// `never-established`, so without the emitted refusal this arm would pass on a reader that had
// simply not dialled yet.
//
// THE POLL IS THREE-STATE, and the third state is the reason it is written out rather than folded
// into a boolean:
//   • the target state    — reached. A verdict about the channel, and the pass.
//   • `not-within-budget` — the surface answered, over and over, and the channel never got there
//                           inside the budget. Also a verdict about the channel, and a real failure.
//   • `probe-read-failed` — the surface stopped answering, answered a non-200, answered something
//                           that is not a status, reported that its own witness raised, or ACCEPTED
//                           THE CONNECTION AND STALLED past the budget. NOT a verdict about the
//                           channel: this script learned nothing about the handshake either way.
//                           Collapsing it into `not-within-budget` would report a dead dev server as
//                           a broken bus, which is how an outage in the apparatus gets filed as a
//                           defect in the subject.
// All three are failures of a run; only the first two say anything about the system.
//
// EVERY WAIT IS BOUNDED, INCLUDING THE BODY — and this is what makes the classification above
// reachable rather than merely written down. A deadline that is only consulted after an awaited call
// RETURNS bounds nothing: a surface that accepts the connection and then stalls produces no verdict
// at all, just a hang, and a hang is the one outcome three-state naming cannot express. So each
// request carries an abort signal derived from the time LEFT in the budget it runs under, and the
// signal stays attached through the response BODY — the sneakier half, where headers arrive, the
// exchange looks alive, and `json()` never returns. A cut-off read is classified as
// `probe-read-failed`, never as `not-within-budget`: the budget expiring mid-read means this script
// could not read, which is not the same claim as the channel failing to arrive. `not-within-budget`
// is reached only from a deadline check made BEFORE a read is issued, with every earlier read
// completed — so the two can never be confused for one another.
//
// THE JOB'S `timeout-minutes` IS THE BACKSTOP BEHIND ALL OF THAT, not a substitute for it. The
// script's own worst case is 3 minutes of budget — 90s boot + 30s healthy arm + 30s scene action +
// 30s armed arm — and the job's timeout sits far above that so the script always gets to produce its
// own classified verdict first. CI steps in only if this script itself wedges somewhere no signal
// reaches, and then the failure says "the job timed out" rather than pretending to be a finding.
//
// BUDGETS, AND THE MEASUREMENTS BEHIND THEM. Measured against the built demo under `wrangler dev`
// (Apple silicon, local Miniflare, seven runs): workerd answered the first status read 5193ms after
// spawn on the first run of a session and 1055-2077ms on every run after it. The healthy channel
// read `established` on that FIRST status read every time (4-7ms of polling, 1 read), and the armed
// arm reached `never-established` carrying one `bus_dial_failed` on ITS first read (6-8ms, 1 read).
// Neither arm has ever needed a second poll on this machine; the polling exists for the runner that
// does. The whole lane costs about 3s of work there — a ~1.0s `react-router build` plus a ~2.1s
// witness — on top of the checkout/install every job in this workflow already pays.
//
// So the budgets are 90s for the boot and 30s for a channel — an order of magnitude above the worst
// number measured, because this lane is allowed to be slow and is not allowed to be flaky, and a
// shared CI runner is slower than the machine those numbers came from. Nothing here is tuned to the
// margin; if a budget ever has to move, move it under the protocol below rather than by feel.
//
// THE FLAKE PROTOCOL, WRITTEN DOWN BEFORE ANYONE NEEDS IT — a protocol invented under a red light is
// a protocol that says "re-run it".
//   1. A first flake is DIAGNOSED, never re-rolled. The discriminator: re-run the failing arm in the
//      SAME runner, then on a FRESH one. An in-runner retry that re-fails while a fresh runner
//      passes is positive evidence of STATE — a port still held, a stale build output, a leftover
//      Miniflare directory — and not of timing. Timing looks like the opposite: the in-runner retry
//      passes.
//   2. If the diagnosis IS a timing budget, the budget is tuned ONCE, from the number the failure
//      itself measured, and that measurement is written into the paragraph above beside the others.
//      Once, from a number — not upward until green.
//   3. A SECOND timing flake after that tune demotes this lane out of the required set to scheduled
//      plus tag-ritual, with a tracked ticket. That path is written here, in daylight, precisely so
//      that it is never invented under pressure by whoever is holding the red light.
//
// IT BOOTS A BUILD, IT DOES NOT MAKE ONE. `pnpm -C apps/demo run build` runs first, as its own step
// — in CI and in the demo's README alike. Two reasons, and the second is the one that matters: the
// build output is gitignored, so this script would otherwise be documented as a command that cannot
// work on a clean checkout; and a build failure and a witness failure are DIFFERENT FINDINGS, which
// a single step would merge into one red. The preflight below turns the missing build into its own
// named verdict rather than letting it surface as a confusing complaint from wrangler.
//
// NO CLOUD CREDENTIALS: Miniflare backs the KV namespace and the Durable Object locally.

import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const DEMO = join(ROOT, "apps/demo");
/** The wrangler config the BUILD emits — the flags and bindings the app actually ships with, not a
 * copy in this file that could drift from them. */
const BUILT_CONFIG = join(DEMO, "build/server/wrangler.json");
/** Distinct from the 8788 the two other boot gates use, so a stray neighbour cannot be mistaken for
 * this one on a shared machine. */
const PORT = 8789;
const BASE_URL = `http://localhost:${PORT}`;
const STATUS_URL = `${BASE_URL}/status.json`;
/** The demo's scene actions are plain form posts; `bus-drop` is the fault this lane arms. */
const SCENE_URL = `${BASE_URL}/scene/bus-drop`;

/** workerd cold-start budget — see the measurements above. */
const BOOT_BUDGET_MS = 90_000;
/** How long a channel is given to reach the state an arm requires. */
const CHANNEL_BUDGET_MS = 30_000;
/** How long a scene action (`arm`, `heal`) is given to be accepted. Its own budget rather than a
 * borrowed one: driving the demo and reading the gauge are different operations and a shared number
 * would hide which of them was slow. */
const ACTION_BUDGET_MS = 30_000;
const POLL_INTERVAL_MS = 500;

const ESTABLISHED = "established";
const NEVER_ESTABLISHED = "never-established";
const DIAL_FAILED = "bus_dial_failed";

/** The two outcomes that are not the target state. Named, because the difference between them is
 * the difference between a finding and an apparatus failure. */
const NOT_WITHIN_BUDGET = "not-within-budget";
const PROBE_READ_FAILED = "probe-read-failed";

const failures = [];
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ── Preflight: the build this script boots must already exist ────────────────
//
// AN UNMET PRECONDITION IS ITS OWN VERDICT, checked here rather than discovered downstream. The
// build output is gitignored, so a fresh checkout has none — and without this, the missing path
// travelled all the way into wrangler and came back as wrangler's complaint about an entry point,
// which reads like a broken app rather than a step nobody ran. Same discipline as the three-state
// poll one layer down: name what actually happened, and never let one failure wear another's face.
//
// IT DOES NOT BUILD FOR YOU, and that restraint is deliberate. The build and the witness are
// separate steps in CI because a build failure and a witness failure are different findings, and
// the step boundary is what keeps them distinguishable — folding the build in here would collapse
// "the app does not compile" into "the app did not establish its channel", which is the one
// conflation this lane exists to avoid. So it names the command instead of running it.
if (!existsSync(BUILT_CONFIG)) {
  console.error(
    `::error::the built demo is missing: ${BUILT_CONFIG} does not exist, so there is nothing for workerd to boot and neither arm can run. The build output is gitignored, so a fresh checkout never has one. Build it first, as the CI job does in its own step:\n\n    pnpm -C apps/demo run build\n    node scripts/demo-boot-witness.mjs\n\nThe build stays a separate step on purpose: a build failure and a witness failure are different findings.`
  );
  process.exit(1);
}

// ── The dev server, and the promise that nothing outlives this process ───────

const child = spawn(
  "pnpm",
  ["exec", "wrangler", "dev", "-c", BUILT_CONFIG, "--port", String(PORT)],
  {
    cwd: DEMO,
    // Its OWN process group. `pnpm exec wrangler` forks workerd, and signalling only the process
    // this script can see leaves the runtime holding the port — an orphan that turns the next run
    // into a mystery. The group kill below takes the whole tree.
    detached: true,
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

let stopped = false;
/** Kill the whole group, once, from whichever exit path gets here first. */
function stopServer() {
  if (stopped || child.pid === undefined) {
    return;
  }
  stopped = true;
  try {
    process.kill(-child.pid, "SIGTERM");
  } catch {
    // Already gone — the only expected reason signalling the group fails.
  }
}

// EVERY exit path, not just the happy one: a normal return, an assertion failure, an uncaught throw
// and an interrupt all land on `exit`, and the two signals are re-raised as a failure so a cancelled
// run never reads as a pass.
process.on("exit", stopServer);
for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => {
    stopServer();
    process.exit(1);
  });
}

// ── Reading the gauge ───────────────────────────────────────────────────────

/** Did this rejection come from the budget running out, rather than from the surface saying
 * something? `AbortSignal.timeout` rejects with a `TimeoutError`, and that name is the whole
 * difference between "cut off" and "answered badly". */
function isTimeout(error) {
  return error !== null && typeof error === "object" && error.name === "TimeoutError";
}

/**
 * One read of the status surface, bounded by `budgetMs`. EVERY failure here is a read failure
 * carrying a reason — this function never converts a surface that would not answer into a claim
 * about the channel.
 *
 * ONE SIGNAL FOR THE WHOLE EXCHANGE, and the body half is the one worth spelling out. A deadline
 * checked only after an awaited call RETURNS bounds nothing: a surface that accepts the connection
 * and then stalls leaves this script hanging past every budget it advertises, producing none of the
 * three verdicts — which is the precise case the three-state classification exists to name. A
 * stalled BODY is the sneakier half of that: the headers arrive, the response looks alive, and
 * `json()` never returns. The signal is attached to the request AND stays attached to the response
 * stream, so it cuts both, and either cut is classified below as what it is — a read that failed,
 * never a verdict about the channel.
 */
async function readStatus(budgetMs) {
  const signal = AbortSignal.timeout(Math.max(1, budgetMs));
  let response;
  try {
    response = await fetch(STATUS_URL, { signal });
  } catch (error) {
    return {
      ok: false,
      reason: isTimeout(error)
        ? `the status surface did not answer within the ${budgetMs}ms left in this budget — the read was cut off, so it says nothing about the channel`
        : `the status surface did not answer: ${error}`,
    };
  }
  if (!response.ok) {
    return {
      ok: false,
      reason: `the status surface answered ${response.status}`,
    };
  }
  let body;
  try {
    body = await response.json();
  } catch (error) {
    return {
      ok: false,
      reason: isTimeout(error)
        ? `the status surface sent headers and then did not finish its body within the ${budgetMs}ms left in this budget — the read was cut off, so it says nothing about the channel`
        : `the status surface answered something that is not JSON: ${error}`,
    };
  }
  if (body === null || typeof body !== "object") {
    return {
      ok: false,
      reason: `the status surface answered ${JSON.stringify(body)}, which is not a status`,
    };
  }
  if (body.channel === null) {
    // The surface reports that its OWN witness raised, so it has no channel to give. That is a read
    // failure and not a channel verdict, for exactly the reason the witness reports it that way.
    return {
      ok: false,
      reason: `the status surface could not read a channel — its witness raised: ${body.witnessError}`,
    };
  }
  if (typeof body.channel !== "string" || !Array.isArray(body.events)) {
    return {
      ok: false,
      reason: `the status surface answered a shape this probe does not recognise: ${JSON.stringify(body).slice(0, 300)}`,
    };
  }
  return { ok: true, status: body };
}

/**
 * Wait for the dev server to answer AT ALL. Transport failures are expected here and only here — a
 * cold workerd start is not a probe failure, it is a cold start. Everything after this point treats
 * a failed read as `probe-read-failed`.
 */
async function awaitSurface() {
  const startedMs = Date.now();
  const deadline = startedMs + BOOT_BUDGET_MS;
  let lastReason = "no read attempted";
  for (;;) {
    // The time LEFT is what each attempt is bounded by, so a stalled attempt cannot outlive the
    // budget the boot advertises — it is cut off exactly when the budget ends.
    const remainingMs = deadline - Date.now();
    if (remainingMs <= 0) {
      return {
        ok: false,
        reason: `the status surface never answered within ${BOOT_BUDGET_MS}ms — last attempt: ${lastReason}`,
      };
    }
    if (child.exitCode !== null) {
      return {
        ok: false,
        reason: `wrangler exited with code ${child.exitCode} before serving anything`,
      };
    }
    const read = await readStatus(remainingMs);
    if (read.ok) {
      return { ...read, afterMs: Date.now() - startedMs };
    }
    lastReason = read.reason;
    await sleep(POLL_INTERVAL_MS);
  }
}

/**
 * Poll until the channel reaches `target` and any `requirement` the arm names also holds.
 *
 * THREE-STATE (see this file's header): the verdict is `target`, `not-within-budget`, or
 * `probe-read-failed`, and the last of those is not a statement about the channel.
 */
async function pollForChannel(target, requirement) {
  const startedMs = Date.now();
  const deadline = startedMs + CHANNEL_BUDGET_MS;
  let reads = 0;
  let last = null;
  for (;;) {
    const remainingMs = deadline - Date.now();
    if (remainingMs <= 0) {
      // The budget is spent and every read COMPLETED — the surface kept answering and the channel
      // did not get there. That is a verdict about the channel. It is reached only here, on a
      // deadline checked BEFORE a read is issued, so a read that was cut off mid-flight can never
      // arrive at it: that path returns `probe-read-failed` below, and the two must not be swapped.
      return last === null
        ? {
            afterMs: Date.now() - startedMs,
            reads,
            reason: `no read completed inside the ${CHANNEL_BUDGET_MS}ms channel budget`,
            verdict: PROBE_READ_FAILED,
          }
        : {
            afterMs: Date.now() - startedMs,
            reads,
            status: last,
            verdict: NOT_WITHIN_BUDGET,
          };
    }
    // Bounded by the time LEFT in this arm's budget, so a stalled surface is cut off at the budget
    // rather than hanging past it — see `readStatus`.
    const read = await readStatus(remainingMs);
    reads += 1;
    if (!read.ok) {
      // The READ is attempted first and the child inspected second, so a dead server ENRICHES the
      // reason rather than replacing it. Either way the verdict is the same one: this probe could
      // not read, so it has nothing to say about the handshake — including when what stopped it was
      // its own budget expiring mid-read.
      const died =
        child.exitCode === null
          ? ""
          : ` (the dev server exited with code ${child.exitCode})`;
      return {
        afterMs: Date.now() - startedMs,
        reads,
        reason: `${read.reason}${died}`,
        verdict: PROBE_READ_FAILED,
      };
    }
    last = read.status;
    if (last.channel === target && (requirement?.holds(last) ?? true)) {
      return { afterMs: Date.now() - startedMs, reads, status: last, verdict: target };
    }
    await sleep(POLL_INTERVAL_MS);
  }
}

/**
 * Drive one of the demo's scene actions — `arm` or `heal` — as the console's own form does. Bounded
 * for the same reason every read is: the action renders a full page, so a stall here would hang the
 * run between the two arms, where no budget was watching at all.
 */
async function postIntent(intent) {
  const signal = AbortSignal.timeout(ACTION_BUDGET_MS);
  try {
    const response = await fetch(SCENE_URL, {
      body: new URLSearchParams({ intent }),
      method: "POST",
      signal,
    });
    // Drain under the SAME signal, so a body that never completes cannot outlive the budget either.
    await response.text();
    return response.ok
      ? { ok: true }
      : { ok: false, reason: `the scene action answered ${response.status}` };
  } catch (error) {
    return {
      ok: false,
      reason: isTimeout(error)
        ? `the scene action did not complete within ${ACTION_BUDGET_MS}ms — the demo accepted the connection and did not finish, so the fault was never armed`
        : `the scene action did not answer: ${error}`,
    };
  }
}

/** Render one arm's outcome as a line, whichever of the three states it landed in. */
function describe(label, result) {
  if (result.verdict === PROBE_READ_FAILED) {
    return `${label}: ${PROBE_READ_FAILED} after ${result.afterMs}ms over ${result.reads} read(s) — ${result.reason}`;
  }
  if (result.verdict === NOT_WITHIN_BUDGET) {
    return `${label}: ${NOT_WITHIN_BUDGET} after ${result.afterMs}ms over ${result.reads} read(s) — the surface kept answering and the channel stayed \`${result.status.channel}\` (dial failures: ${result.status.dialFailures}; events: ${result.status.events.join(", ") || "none"})`;
  }
  return `${label}: \`${result.verdict}\` reached after ${result.afterMs}ms over ${result.reads} read(s) — dial failures: ${result.status.dialFailures}; events: ${result.status.events.join(", ") || "none"}`;
}

// ── The run ─────────────────────────────────────────────────────────────────

const startedMs = Date.now();
const lines = [];

const boot = await awaitSurface();
if (boot.ok) {
  lines.push(`boot: the status surface answered after ${boot.afterMs}ms`);

  // ARM 1 — HEALTHY. The claim this lane exists for: the app as shipped completes its handshake.
  const healthy = await pollForChannel(ESTABLISHED, null);
  lines.push(describe("healthy arm", healthy));
  if (healthy.verdict !== ESTABLISHED) {
    failures.push(
      `the composed demo did not establish its bus channel on workerd — ${describe("healthy arm", healthy)}`
    );
  }

  // ARM 2 — ARMED, this lane's own negative control. Without it the arm above could be satisfied by
  // a surface that says `established` no matter what is true.
  const armed = await postIntent("arm");
  if (armed.ok) {
    const refused = await pollForChannel(NEVER_ESTABLISHED, {
      holds: (status) =>
        status.dialFailures > 0 && status.events.includes(DIAL_FAILED),
    });
    lines.push(describe("armed arm", refused));
    if (refused.verdict !== NEVER_ESTABLISHED) {
      failures.push(
        `the armed \`bus-drop\` fault did not produce a refused channel — so this run's healthy arm proves nothing: a surface that cannot report the negative cannot be trusted to have reported the positive. ${describe("armed arm", refused)}`
      );
    }
  } else {
    failures.push(
      `${PROBE_READ_FAILED} — the \`bus-drop\` scene could not be armed, so the negative control never ran: ${armed.reason}`
    );
  }
} else {
  failures.push(
    `${PROBE_READ_FAILED} — ${boot.reason}. Neither arm ran, so this run establishes nothing about the channel. Last output:\n${log}`
  );
}

stopServer();

const elapsedMs = Date.now() - startedMs;

if (failures.length > 0) {
  for (const line of lines) {
    console.error(line);
  }
  for (const failure of failures) {
    console.error(`::error::${failure}`);
  }
  console.error(`demo-boot-witness: FAILED in ${elapsedMs}ms.`);
  process.exit(1);
}

console.log(
  [
    "demo-boot-witness: the composed demo booted on workerd and established its bus channel, and the",
    "same instrument reported the refused channel when the `bus-drop` fault was armed — so the green",
    "above is a reading rather than a surface stuck on one word.",
    ...lines.map((line) => `  ${line}`),
    `  run: ${elapsedMs}ms (boot + both arms; the build is a separate step)`,
    "  NOT proven here: bus mechanism correctness (the do-bus integration lane's clause) or",
    "  convergence timing (a later measurement phase's). See this script's header.",
  ].join("\n")
);
