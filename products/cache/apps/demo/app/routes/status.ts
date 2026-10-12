// The demo's JSON STATUS SURFACE — a machine-readable view of the one fact a boot probe needs.
//
// WHY IT EXISTS. `scripts/demo-boot-witness.mjs` holds a claim about this app: booted on real
// workerd against the real Coordinator Durable Object, the replication reader's bus channel reaches
// `established`. Something has to report that channel to a script. The console already renders it —
// but a probe that parsed the rendered cell would be reading MARKUP as an instrument, and the first
// time the table's layout moved the probe would read a blank and never say so. A gauge that can go
// wrong without going red is worse than no gauge, so the reading gets a surface of its own with a
// shape a script can check.
//
// IT REPORTS, IT DOES NOT COMPUTE. `channel` is `explain().reader.channel` verbatim, obtained
// through the same `witnessProbe` call the console's channel row renders from. There is deliberately
// no second derivation of the channel anywhere in this app: two ways of working out one fact is two
// facts, and the day they disagree the page and the probe will each be certain.
//
// WHY IT READS BEFORE IT WITNESSES, in the same order `routes/home.tsx` does. `explain()` observes;
// it does not open a channel. The subscription is deferred to first USE (E28) — a read, a purge —
// so a surface that only witnessed would report `never-established` on an isolate nobody had read
// from, and that is a fact about the probe's own traffic rather than about the deployment. So this
// route does what a page does: it performs the app's one read, then witnesses it. What it reports is
// therefore the channel of an app that has actually been used, which is the only channel worth
// asking about.
//
// NOT AN OPERATIONS ENDPOINT. It carries what a boot probe asserts and nothing else. A status
// surface with room in it grows a second meaning, and then a second consumer, and then it cannot be
// changed.

import type { ChannelState, TelemetryEventName } from "@astilba/cache";
import { TELEMETRY_EVENTS } from "@astilba/cache";
import { cacheContext } from "@astilba/cache/react-router";

import type { SceneId } from "../../chaos/script";
import { readProbe, witnessProbe } from "../probe";
import { worldContext } from "../world";
import type { Route } from "./+types/status";

/** What this surface answers with. Narrow on purpose — see the note at the top of the file. */
interface DemoStatus {
  /** The scene armed right now, or `null` for the healthy system. */
  readonly armed: SceneId | null;
  /**
   * The reader's channel as `explain()` declared it, or `null` when the witness RAISED — in which
   * case there is no channel reading to report and this surface says so rather than picking one. A
   * consumer that treats `null` as "not established" would be converting "I could not look" into an
   * answer, which is the one thing the whole channel row exists to stop.
   */
  readonly channel: ChannelState | null;
  /** How many `bus_dial_failed` facts the library has emitted this episode — the evidence that a
   * channel which never came up was REFUSED rather than merely newborn. */
  readonly dialFailures: number;
  /** Every telemetry event type this episode's sink received, oldest first. */
  readonly events: readonly TelemetryEventName[];
  /** What the witness raised, when it raised. `null` on every ordinary path. */
  readonly witnessError: string | null;
}

export async function loader({ context }: Route.LoaderArgs): Promise<Response> {
  const world = context.get(worldContext);
  const cache = context.get(cacheContext);
  // The app's own read, first — the same call the console's loader makes, and the one that opens the
  // channel. Its result is not reported here: what this surface is for is the state the read leaves
  // behind, which the witness below is the only honest reporter of.
  await readProbe(cache);
  const witness = await witnessProbe(cache);
  const events = world.events.map((event) => event.type);
  const body: DemoStatus = {
    armed: world.armed()?.id ?? null,
    channel: witness.ok ? witness.explanation.reader.channel : null,
    dialFailures: events.filter(
      (type) => type === TELEMETRY_EVENTS.bus_dial_failed
    ).length,
    events,
    witnessError: witness.ok ? null : witness.error,
  };
  return Response.json(body);
}
