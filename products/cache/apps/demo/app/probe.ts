// The one entry this demo caches, and the read it performs (C5-c).
//
// One key, one tag, one factory. A demo with a rich data model would spend the reader's attention
// on the data model; the whole subject here is what happens to ONE entry when the machinery under
// it fails, so there is exactly one entry and it is boring on purpose.

import type { Cache, CacheEntry, Explanation, Tag } from "@astilba/cache";
import { t } from "@astilba/cache";
import { currentRequest } from "@astilba/cache/react-router";

/** The key the console reads and `explain()` witnesses. */
const PROBE_KEY = "demo:headline";

/** The tag the console purges to show invalidation travelling (or failing to). */
export const PROBE_TAG: Tag = t`headline`;

/** How many times the factory has actually run this isolate — the cheapest possible way to see
 * whether a read was served or filled, and one the cache cannot fake for us. */
let factoryRuns = 0;

/** The factory's run count for this isolate. */
export function factoryRunCount(): number {
  return factoryRuns;
}

/** The result of the console's read: either the entry, or the error the read raised. A fault can
 * legitimately surface as a thrown read, and a demo that swallowed that would be hiding the most
 * honest thing on the page.
 *
 * `readMs` and `filled` ride on BOTH arms because both are true either way: a read that threw still
 * took time, and a read that threw after running the factory still ran the factory. */
export type ProbeResult = {
  /** What the whole read cost, wall-clock, at this app's edge. */
  readonly readMs: number;
  /** Did the factory run during THIS read — i.e. was it a fill? Taken from the run counter on
   * both sides of the call, which is the one thing here the cache cannot fake for us. */
  readonly filled: boolean;
} & (
  | { readonly ok: true; readonly entry: CacheEntry<string> }
  | { readonly ok: false; readonly error: string }
);

/**
 * What `explain()` reported, or the error it raised trying.
 *
 * THE RAISE ARM IS A GUARD, NOT A SCENE — and it used to be the other way round, which is worth
 * writing down because the page's best-known paragraph was built on it. `explain` was documented
 * never to throw for a MISSING key, and that promise is about absence; an unreadable store is not
 * absence, so a scene that broke the store broke the witness, and the witness raised. The demo
 * printed the raise and explained, at length, that "I could not look" and "it is not there" are
 * different facts.
 *
 * They are — and the library now says so itself. A suppressed store read reports as its own
 * `read-failed` arm of the `Explanation` union (E13's read half, E27): no tier, no identity, and no
 * pretence of absence. So the distinction this app used to narrate around a throw is now a state the
 * witness returns, and none of this demo's dials can make `explain` raise: the KV adapter's read
 * classifier is TOTAL, so every rejection it sees is named and suppressed rather than propagated.
 *
 * The arm stays because an app at the edge of a platform does not get to assume a promise holds —
 * an unclassified failure from somewhere nobody thought of would land here, and a page that crashed
 * on it would be worse than one that printed it. It is a guard against the unenumerated, not a
 * scene, and the narration says so where it prints it.
 */
export type WitnessResult =
  | { readonly ok: true; readonly explanation: Explanation }
  | { readonly ok: false; readonly error: string };

/** Witness the probe key, surfacing whatever happened. */
export async function witnessProbe(cache: Cache): Promise<WitnessResult> {
  try {
    return { explanation: await cache.explain(PROBE_KEY), ok: true };
  } catch (error: unknown) {
    return {
      error:
        error instanceof Error
          ? `${error.name}: ${error.message}`
          : String(error),
      ok: false,
    };
  }
}

/**
 * Read the probe entry through the full published path: the request's principal-bearing context
 * from the middleware's ALS frame (which is what the kernel derives scope from), the entry form of
 * the read (which reports the tier it was served from, whether the value was graced, whether it was
 * served on error, and whether the write-back was durable), and the tag that makes it purgeable.
 */
export async function readProbe(cache: Cache): Promise<ProbeResult> {
  const runsBefore = factoryRuns;
  // PLATFORM: measure request duration using the runtime clock.

  const startedMs = Date.now();
  try {
    const entry = await cache.getOrSetEntry<string>({
      factory: async () => {
        factoryRuns++;
        return await Promise.resolve(
          `headline #${factoryRuns} — filled by the origin`
        );
      },
      key: PROBE_KEY,
      request: currentRequest(),
      tags: [PROBE_TAG],
    });
    return {
      entry,
      filled: factoryRuns > runsBefore,
      ok: true,
      // PLATFORM: the wall clock, at the app's edge.
      readMs: Date.now() - startedMs,
    };
  } catch (error: unknown) {
    return {
      error:
        error instanceof Error
          ? `${error.name}: ${error.message}`
          : String(error),
      filled: factoryRuns > runsBefore,
      ok: false,
      // PLATFORM: the wall clock, at the app's edge.
      readMs: Date.now() - startedMs,
    };
  }
}
