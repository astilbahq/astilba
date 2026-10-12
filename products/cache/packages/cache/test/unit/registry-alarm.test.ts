import { earliestAlarm } from "@astilba/cache/registry";
import { describe, expect, test } from "vitest";

// Unit teeth for earliest-wins alarm arbitration (§4.2, B5, D11) — the pure decision behind the
// Coordinator's ONE alarm slot serving two deadlines (the B5 flush window + the E5 heartbeat
// cadence). The real Coordinator lives on the workerd lane and cannot be unit-tested Node-side, so
// this pins the pure kernel it delegates to (and the integration heartbeat suite proves the wiring).
//
// The two failure modes it must beat — one per old code path:
//   - `return existing` (FIRST-writer-wins): the old `#armFlushAlarm` returned early whenever an
//     alarm was set, so a far heartbeat deadline armed first BLOCKED a nearer flush.
//   - `return desired` (LAST-writer-wins): the old heartbeat re-arm `setAlarm(now + interval)`
//     OVERRODE a nearer flush deadline already armed.
// Nearest-wins (`min`) beats both. The tests below fail under each, and under a `Math.max` slip.

describe(earliestAlarm, () => {
  test("flush deadline sooner than the armed heartbeat ⇒ the flush wins (beats first-writer-wins)", () => {
    const heartbeatDeadline = 30_000;
    const flushDeadline = 1100;
    // A heartbeat is armed; a nearer flush deadline arrives. `return existing` (the old
    // `#armFlushAlarm`) would keep 30_000 and starve the flush — nearest wins forbids it.
    expect(earliestAlarm(heartbeatDeadline, flushDeadline)).toBe(flushDeadline);
  });

  test("heartbeat deadline sooner than the armed flush ⇒ the heartbeat wins (the converse)", () => {
    const flushDeadline = 9900;
    const heartbeatDeadline = 5000;
    // The symmetric direction: a flush is armed; a nearer heartbeat arrives. Nearest still wins.
    expect(earliestAlarm(flushDeadline, heartbeatDeadline)).toBe(
      heartbeatDeadline
    );
  });

  test("nothing armed yet ⇒ the desired deadline is taken outright", () => {
    expect(earliestAlarm(null, 1100)).toBe(1100);
    expect(earliestAlarm(null, 30_000)).toBe(30_000);
  });

  test("an armed nearer flush is NOT overridden by a later heartbeat re-arm (beats last-writer-wins)", () => {
    // THE named mutation teeth: `setAlarm(now + interval)` last-writer-wins would push the armed
    // 1100 flush deadline out to 30_000 and breach the honest ≤1.1s bound. `return desired` gets
    // this exact case wrong; nearest wins keeps 1100.
    expect(earliestAlarm(1100, 30_000)).toBe(1100);
    expect(earliestAlarm(1100, 30_000)).not.toBe(30_000);
  });
});
