// Ordinary unit teeth (NOT sacred). The shared `safeEmit` swallow-wrap and
// its injectable `onSinkError` hook, extracted from the poll driver. Each case bites one branch
//:
//   • sync-throw guard   — a sink that THROWS synchronously is swallowed (caller unaffected) and the
//     hook fires once. Drop the `try/catch` ⇒ the throw reaches the caller ⇒ reds.
//   • async-reject guard — a sink that returns a REJECTING thenable is adopted-and-swallowed (no
//     unhandled rejection) and the hook fires once. Drop the `.catch` adoption ⇒ a rejection floats
//     ⇒ reds.
//   • onSinkError no-latch — the hook is INJECTED, not a module global: two independent emit sites,
//     each with its own hook, never cross-talk. Don't-call-the-hook ⇒ reds.
//   • well-behaved sink   — delivered exactly once (the guard costs nothing on the happy path).

import { setImmediate } from "node:timers/promises";

import { describe, expect, it } from "vitest";

import { safeEmit, TELEMETRY_EVENTS } from "../../src/telemetry";
import type { TelemetryEvent } from "../../src/telemetry";

const EVENT: TelemetryEvent = { type: TELEMETRY_EVENTS.private_evicted };

/** A sink that throws SYNCHRONOUSLY — a bad `JSON.stringify`, a dead transport reached eagerly. */
function syncThrowingSink(): void {
  throw new Error("sink threw synchronously");
}

/** An `async` sink whose transport is down: it type-checks against `TelemetrySink` (`(e) => void`
 * accepts any return), so an app can ship it, and what it hands back is a REJECTING promise. */
async function rejectingAsyncSink(): Promise<void> {
  await Promise.resolve();
  throw new Error("sink transport is down");
}

/** An `async` onSinkError hook that REJECTS. `SinkErrorHook` is `(e) => void`, which accepts an
 * async fn (a `void` return accepts any value), so an app can ship exactly this — the hook is the
 * last swallow-chain link, and its rejection must be adopted, not merely sync-guarded. */
async function rejectingAsyncHook(): Promise<void> {
  await Promise.resolve();
  throw new Error("the hook rejects asynchronously");
}

/**
 * Run `body` while capturing PROCESS-LEVEL unhandled rejections — the failure an `async` sink's
 * rejected promise causes, invisible to a plain `await` (the caller returns; the sink's promise
 * floats away separately). Node reports an un-adopted rejection at the end of the turn, so the
 * macrotask hop is what gives it the chance to fire.
 */
async function unhandledRejectionsDuring(
  body: () => void | Promise<void>
): Promise<unknown[]> {
  const escaped: unknown[] = [];
  const onUnhandled = (reason: unknown): void => {
    escaped.push(reason);
  };
  process.on("unhandledRejection", onUnhandled);
  try {
    await body();
    await setImmediate(); // one macrotask turn: Node's unhandled-rejection sweep runs here
    return escaped;
  } finally {
    process.off("unhandledRejection", onUnhandled);
  }
}

describe("safeEmit — the shared swallow-wrap (CACHE-21, D2)", () => {
  it("a SYNC-throwing sink is swallowed: the caller is unaffected, and onSinkError fires once", () => {
    const errors: unknown[] = [];
    // The contract: safeEmit returns normally (no throw) even though the sink threw. Drop the
    // try/catch in safeEmit and this call throws ⇒ reds.
    expect(() => {
      safeEmit(syncThrowingSink, EVENT, (caught) => {
        errors.push(caught);
      });
    }).not.toThrow();
    expect(errors).toHaveLength(1);
    expect(errors[0]).toBeInstanceOf(Error);
  });

  it("a REJECTING async sink floats NOTHING, and onSinkError fires once", async () => {
    const errors: unknown[] = [];
    const escaped = await unhandledRejectionsDuring(() => {
      // oxlint-disable-next-line typescript/no-misused-promises, typescript/strict-void-return -- DELIBERATE: `rejectingAsyncSink` IS the async-sink defect under test. `TelemetrySink` is `(e) => void`, and TypeScript ACCEPTS an async fn there (a `void` return accepts any value), so a consumer can ship exactly this — safeEmit must survive it at runtime, which is what this case proves.
      safeEmit(rejectingAsyncSink, EVENT, (caught) => {
        errors.push(caught);
      });
    });
    // The rejection was ADOPTED and swallowed — nothing floated. Drop the `.catch` adoption in
    // safeEmit and the raw rejected promise escapes ⇒ `escaped` is non-empty ⇒ reds.
    expect(escaped).toStrictEqual([]);
    // The hook still observed the swallowed failure — reported asynchronously, off the adopted
    // rejection (so this runs after the macrotask hop above).
    expect(errors).toHaveLength(1);
    expect(errors[0]).toBeInstanceOf(Error);
  });

  it("onSinkError is INJECTED, not a module latch: two emit sites never cross-talk (D4)", () => {
    // Two independent caches/registries would each pass their OWN hook. Model that directly: two
    // hooks, two emits — each hook sees exactly its own failure, never the other's. A module-level
    // latch (the design) would let the first emit silence the second, or route both to
    // one place ⇒ this reds.
    const errorsA: unknown[] = [];
    const errorsB: unknown[] = [];
    safeEmit(syncThrowingSink, EVENT, (caught) => {
      errorsA.push(caught);
    });
    safeEmit(syncThrowingSink, EVENT, (caught) => {
      errorsB.push(caught);
    });
    expect(errorsA).toHaveLength(1);
    expect(errorsB).toHaveLength(1);
  });

  it("a well-behaved sink is delivered exactly once (the guard costs nothing on the happy path)", () => {
    const events: TelemetryEvent[] = [];
    let hookCalls = 0;
    safeEmit(
      (event) => {
        events.push(event);
      },
      EVENT,
      () => {
        hookCalls += 1;
      }
    );
    expect(events).toStrictEqual([EVENT]);
    expect(hookCalls).toBe(0); // no failure ⇒ the hook is never called
  });

  it("an undefined sink is a no-op (no throw, hook never called)", () => {
    let hookCalls = 0;
    expect(() => {
      safeEmit(undefined, EVENT, () => {
        hookCalls += 1;
      });
    }).not.toThrow();
    expect(hookCalls).toBe(0);
  });

  it("a THROWING onSinkError hook cannot break safeEmit's never-throws contract", () => {
    // The hook is app code at the very END of the swallow chain; if IT throws, there is no
    // meta-channel — safeEmit must still not throw. (Reached via a sync-throwing sink.)
    expect(() => {
      safeEmit(syncThrowingSink, EVENT, () => {
        throw new Error("the hook itself is broken");
      });
    }).not.toThrow();
  });

  it("an ASYNC-REJECTING onSinkError hook floats NOTHING (the hook is arbitrary app code too)", async () => {
    // The hook has a `void` return, so — exactly like the sink — an `async` hook type-checks and
    // can REJECT. Its rejection must be adopted, not merely sync-guarded, or it floats as an
    // unhandled rejection (a process-terminating escape one link past the sink). Reached via a
    // sync-throwing sink so the hook fires on the error path.
    const escaped = await unhandledRejectionsDuring(() => {
      // oxlint-disable-next-line typescript/no-misused-promises, typescript/strict-void-return -- DELIBERATE: `rejectingAsyncHook` IS the async-hook defect under test. `SinkErrorHook` is `(e) => void`, and TypeScript ACCEPTS an async fn there, so a consumer can ship exactly this — reportSinkError must adopt its rejection, which is what this case proves.
      safeEmit(syncThrowingSink, EVENT, rejectingAsyncHook);
    });
    // The hook's rejection was ADOPTED (reportSinkError's `.catch`); nothing floated. Revert
    // reportSinkError to the sync-only try/catch and the raw rejection escapes ⇒ `escaped` non-empty ⇒ reds.
    expect(escaped).toStrictEqual([]);
  });
});
