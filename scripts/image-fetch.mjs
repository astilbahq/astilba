import { setTimeout as pause } from "node:timers/promises";

// Runs a synchronous step that fetches over the network, making up to three
// attempts within one time limit, which each attempt receives as what remains.
// A step that used up the limit is stuck rather than dropped, so it stops. The
// tools exit alike for any failure, so a repeat may have followed a failed
// integrity check; the annotation keeps that visible on a run that then passes.
export async function withFetchAttempts(
  name,
  limitMs,
  step,
  { sleep = pause, now = Date.now } = {},
) {
  const deadline = now() + limitMs;
  for (const delay of [20_000, 40_000, undefined]) {
    try {
      // A timeout of 0 would mean no limit, and a sleep can overrun what remains.
      return step(Math.max(deadline - now(), 1));
    } catch (error) {
      if (
        delay === undefined ||
        error.code === "ETIMEDOUT" ||
        now() + delay >= deadline
      )
        throw error;
      console.warn(
        `::warning::${name} failed; see the error above. Trying again in ${delay / 1000} seconds`,
      );
      await sleep(delay);
    }
  }
}
