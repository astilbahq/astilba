// Integration-lane factory: a `RegistryHarness` backed by the REAL Coordinator Durable
// Object, for driving the UNMODIFIED Registry conformance contract on workerd (the
// conformance dividend). Each `make()` mints a FRESH Coordinator id so the
// contract's per-check instances are independent. A real driver exposes no fake-only
// `controls`, so the control-gated conformance checks self-skip (as designed).

import { runDurableObjectAlarm } from "cloudflare:test";
import { env } from "cloudflare:workers";

import { doRegistry } from "../../src/adapters/cloudflare/do-registry";
import type { RegistryHarness } from "../conformance/runner/registry";

let counter = 0;

/** A fresh Coordinator-DO-backed `Registry` harness (no controls — real driver). */
export function makeDoRegistry(): RegistryHarness {
  const id = env.COORDINATOR.idFromName(`c1c-registry-${counter++}`);
  const stub = env.COORDINATOR.get(id);
  return { registry: doRegistry(stub) };
}

/** Turns needed on each SUCCESSFUL await — **scoped to ONE TEST FILE, not the run.** This module is
 * re-evaluated per test file, so each file accumulates its own samples and can never see another's.
 * Two files call `fireAlarm`, and their samples never meet.
 *
 * READ THE SCOPE OFF THIS COMMENT BEFORE QUOTING ANY `n`: the array is read in exactly one place —
 * the exhaustion message below — so a file that never exhausts reports nothing at all, and a file
 * that does reports only what it had gathered by then. Every `n` this instrument can produce is
 * therefore one file's, and conditioned on that file having failed. Widening it means a cross-file
 * channel, which this harness deliberately does not have. Numbers and their disposition:. */
const TURNS_ON_SUCCESS: number[] = [];

/**
 * Fire a Durable Object's alarm, having first AWAITED it into existence.
 *
 * `runDurableObjectAlarm` answers `false` when no alarm is scheduled — a verdict about the present
 * instant, not about the system. The writes that schedule an alarm and the test's question about it
 * are separate trips into the object, so the question can arrive first. Asserting `toBe(true)` on
 * the first ask therefore encodes a race (upstream workers-sdk #11031).
 *
 * Poll to the precondition instead: ask, and if no alarm was runnable yet, yield and ask again, up
 * to a bounded budget. A harness must never race the event it exists to observe.
 *
 * **DO NOT TREAT THIS LOOP AS PROTECTION, AND DO NOT TUNE ITS BUDGET FROM THIS INSTRUMENT'S
 * OUTPUT.** The measured distribution and what it does and does not establish live,
 * which owns them; what this file must respect is the consequence: the budget is a placeholder, no
 * observation here licenses raising or lowering it, and tuning from a truncated sample is how a real
 * timing failure gets preserved behind a number that looks measured. A change to `TURNS` needs
 * the discriminating run, not a reading from this loop.
 *
 * Lives here rather than in any one test file because two suites drive the same alarm, and two
 * copies of a timing discipline drift apart silently.
 */
export async function fireAlarm(stub: DurableObjectStub): Promise<void> {
  const TURNS = 50;
  for (let turn = 0; turn < TURNS; turn += 1) {
    // oxlint-disable-next-line no-await-in-loop -- sequential BY DESIGN: each ask is a fresh trip
    // into the object, and the point is to keep asking until one finds an alarm scheduled.
    if (await runDurableObjectAlarm(stub)) {
      TURNS_ON_SUCCESS.push(turn);
      if (turn > 0) {
        // A success that needed waiting is the one observation this harness cannot get from the
        // exhaustion message, so it goes out on its own channel. That channel is `console`, which
        // the pool intercepts by default — so this line reaches a log only under
        // `--disable-console-intercept`, and its SILENCE is therefore not evidence unless that flag
        // was passed. A success needing no wait is the common case and would only be noise.
        console.log(`[fireAlarm] alarm appeared after ${turn} extra turn(s)`);
      }
      return;
    }
    // oxlint-disable-next-line no-await-in-loop -- as above.
    await scheduler.wait(1);
  }
  // WHAT THIS PROVES, and no more: no alarm became runnable within this budget. It does NOT
  // distinguish an alarm that is pathologically late from one that was never scheduled at all.
  // The sample it carries is one FILE's and is conditioned on that file failing — see the
  // accumulator's own comment before quoting any `n` from it.
  throw new Error(
    `[fireAlarm] no alarm became runnable within ${TURNS} turns. This proves only that: the budget ` +
      `is not yet measured, so it does not establish whether the alarm was absent or merely late. ` +
      `Turns-on-success in THIS TEST FILE so far: ${JSON.stringify(TURNS_ON_SUCCESS)} ` +
      `(n=${TURNS_ON_SUCCESS.length}, file-local — other files keep their own). See CACHE-46.`
  );
}
