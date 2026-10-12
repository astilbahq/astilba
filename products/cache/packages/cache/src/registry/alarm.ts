// Earliest-wins alarm arbitration (§4.2, B5, D11) — the pure decision behind the Coordinator's
// ONE Durable Object alarm slot.
//
// Two deadlines compete for that single slot: the B5 flush-window boundary (armed by a purge)
// and the E5 heartbeat cadence (re-armed after each pointer bless). The NEAREST must win, and the
// farther one must NOT be lost — whichever fires re-arms the other. This module is the pure kernel
// of that rule: given the currently-armed alarm (or `null` for none) and a newly-desired deadline,
// return the deadline to arm. The host wires the result into `setAlarm`; the arbitration itself is
// pure (no ambient time — deadlines are parameters), so it lives here in the Coordinator core and
// is unit-tested directly, off the workerd lane.
//
// The defect this closes: the prior heartbeat path called `setAlarm(now + interval)`
// UNCONDITIONALLY (last-writer-wins), overriding a pending B5 flush deadline — so once a heartbeat
// deadline was armed, a later purge's flush waited the whole heartbeat interval instead of the next
// ≤1.1s window, a silent breach of the honest flush bound. Earliest-wins forbids it: an armed
// sooner deadline is never pushed out.

/**
 * The NEAREST of a currently-armed alarm and a newly-desired deadline (§4.2, B5, D11). A `null`
 * existing means nothing is armed yet, so the desired deadline wins outright. Never pushes an armed
 * alarm LATER — the min guarantees a sooner deadline (a due flush) always survives a later one (the
 * idle heartbeat cadence), and re-arming with the sooner value is idempotent.
 */
export function earliestAlarm(
  existing: number | null,
  desired: number
): number {
  return existing === null ? desired : Math.min(existing, desired);
}
