import { cacheContext } from "@astilba/cache/react-router";
import { data, Form, href, Link } from "react-router";

import { isSceneId, sceneOf } from "../../chaos/script";
import { ms, POSTURE } from "../bounds";
import { NarrationTable, ProbePanel } from "../console";
import { HEALTHY_NOTE, narrate } from "../narrate";
import { factoryRunCount, PROBE_TAG, readProbe, witnessProbe } from "../probe";
import { rescueIfArrived, worldContext } from "../world";
import type { Route } from "./+types/scene";

/** The `:sceneId` param arrives as a bare string; this is the boundary that refuses anything that
 * is not one of the three. A 404 for an unknown scene, not a blank page. */
function requireScene(sceneId: string) {
  if (!isSceneId(sceneId)) {
    // oxlint-disable-next-line typescript/only-throw-error -- React Router's own control-flow idiom: `data()` throws a Response the router catches and renders as a 404. It is not an error object and is not meant to be one; throwing an `Error` here would produce a 500 instead of the 404 this is for.
    throw data(`no such scene: ${sceneId}`, { status: 404 });
  }
  return sceneOf(sceneId);
}

export async function action({ context, params, request }: Route.ActionArgs) {
  const scene = requireScene(params.sceneId);
  const world = context.get(worldContext);
  const cache = context.get(cacheContext);
  const form = await request.formData();
  const intent = form.get("intent");
  if (intent === "arm") {
    world.arm(scene.id);
  }
  if (intent === "heal") {
    world.heal();
  }
  if (intent === "purge" && scene.purgeInstruction !== null) {
    // Purge under the armed fault — the invalidation path is the one the bus and the registry both
    // sit on, so this is the button that makes a bus drop or a Coordinator outage mean something.
    // It can legitimately throw, and the console says so rather than pretending it did not.
    //
    // GATED ON THE SCENE, HERE AND NOT ONLY ON THE BUTTON: a scene that does not instruct this flow
    // does not run it, and a hand-rolled POST is not a licence. The button's absence is the page
    // being honest; this is the page meaning it.
    //
    // The stopwatch starts BEFORE the purge is accepted, marked with the reader's epoch as it stands
    // right now. What later tells the paths apart is that epoch, the epoch straight after the ack,
    // and the reader's CHANNEL at the advance — read off the witness, not off a dial counter.
    const before = await witnessProbe(cache);
    try {
      await cache.expire({ tag: PROBE_TAG });
      // The epoch RIGHT AFTER the ack, and this is the load-bearing measurement: this isolate just
      // asked for the purge, so its own reader may already have been taught by the ack — before any
      // tick, with every dial refused. Reading it here is what stops the demo crediting the poll
      // carrier for a trip nobody made.
      const afterAck = await witnessProbe(cache);
      world.purgeIssued({
        appliedEpochAfterAck: afterAck.ok
          ? afterAck.explanation.reader.appliedEpoch
          : 0,
        appliedEpochAtPurge: before.ok
          ? before.explanation.reader.appliedEpoch
          : 0,
      });
    } catch (error: unknown) {
      return {
        purgeError:
          error instanceof Error
            ? `${error.name}: ${error.message}`
            : String(error),
      };
    }
  }
  return { purgeError: null };
}

export async function loader({ context, params }: Route.LoaderArgs) {
  const scene = requireScene(params.sceneId);
  const world = context.get(worldContext);
  const cache = context.get(cacheContext);
  const probe = await readProbe(cache);
  const witness = await witnessProbe(cache);
  const armed = world.armed();
  const armedForMs =
    armed !== null && armed.id === scene.id ? world.nowMs() - armed.atMs : null;
  // Settle the rescue stopwatch: has the reader's applied epoch moved past where it stood when the
  // purge was accepted? This runs on the READ path because the reader's ticks ride on requests —
  // the observation and the thing observed share the same trigger, which is the carrier's whole
  // design and the reason reloading is what makes this page move.
  const pending = world.pendingPurge();
  const arrived = rescueIfArrived(pending, witness, world.nowMs());
  if (arrived !== null) {
    world.rescueObserved(arrived);
  }
  return {
    armedForMs,
    awaitingRescueForMs:
      arrived === null && pending !== null
        ? world.nowMs() - pending.atMs
        : null,
    factoryRuns: factoryRunCount(),
    isArmed: armed?.id === scene.id,
    narration: narrate({
      armedForMs,
      awaitingRescueForMs:
        arrived === null && pending !== null
          ? world.nowMs() - pending.atMs
          : null,
      busStats: world.busStats,
      coordinatorHeartbeatMs: world.coordinatorHeartbeatMs,
      dials: world.dials,
      events: world.events,
      filled: probe.filled,
      kvStats: world.kvStats,
      readMs: probe.readMs,
      rescue: world.lastRescue(),
      scene,
      witness,
    }),
    onEpisodeReader: world.onEpisodeReader(),
    otherArmed: armed !== null && armed.id !== scene.id ? armed.id : null,
    probe,
    scene: {
      injects: scene.injects,
      mayHappen: scene.mayHappen,
      premise: scene.premise,
      purgeInstruction: scene.purgeInstruction,
      title: scene.title,
    },
  };
}

/** The camera note while a fault is live. Extracted because it is a sentence with a number in it,
 * not a conditional worth reading inside JSX. */
function armedNote(
  armedForMs: number | null,
  onEpisodeReader: boolean
): string {
  const forHowLong = armedForMs === null ? "—" : ms(armedForMs);
  const rig = onEpisodeReader
    ? " This scene booted its OWN reader when you armed it, so the rows below describe a reader whose entire life has been under this fault — that is the rig, and it is why the channel row can say anything at all."
    : "";
  return `Armed for ${forHowLong}.${rig} Suspicion is permitted to take ${ms(POSTURE.suspicionAfterMs)} of OBSERVED silence, and the reader's ticks ride on requests — so reload the page to give it something to observe, and expect nothing before the threshold.`;
}

export default function Scene({
  actionData,
  loaderData,
}: Route.ComponentProps) {
  const {
    armedForMs,
    factoryRuns,
    isArmed,
    narration,
    onEpisodeReader,
    otherArmed,
    probe,
    scene,
  } = loaderData;
  return (
    <main
      style={{
        fontFamily: "system-ui, sans-serif",
        margin: "0 auto",
        maxWidth: "60rem",
        padding: "2rem 1rem",
      }}
    >
      <p>
        <Link to={href("/")}>&larr; every scene</Link>
      </p>
      <h1>{scene.title}</h1>
      <p style={{ color: "#555" }}>{scene.premise}</p>
      <p>
        <strong>The fault:</strong> {scene.injects}. It is applied by this
        demo&rsquo;s own wrapper around the real binding — the library is not
        told, and has no way to ask.
      </p>

      <h2>What the documentation permits to follow</h2>
      <ul>
        {scene.mayHappen.map((line) => (
          <li key={line}>{line}</li>
        ))}
      </ul>

      <h2>Camera</h2>
      <p>
        {isArmed ? armedNote(armedForMs, onEpisodeReader) : HEALTHY_NOTE}
        {otherArmed !== null && (
          <>
            {" "}
            <strong>
              ({otherArmed} is armed instead — arming this one heals that first:
              its dials, its counters, its log, its stopwatch and any reader it
              booted. One reset path, both doors.)
            </strong>
          </>
        )}
      </p>
      {scene.purgeInstruction !== null && (
        <p style={{ color: "#555" }}>{scene.purgeInstruction}</p>
      )}
      <Form method="post" style={{ display: "flex", gap: "0.5rem" }}>
        <button name="intent" type="submit" value="arm">
          Arm the fault
        </button>
        {/* A BUTTON IS AN INSTRUCTION. A scene that does not instruct a purge does not offer one —
            cutting the sentence and leaving the control would be cutting the caption, not the flow. */}
        {scene.purgeInstruction !== null && (
          <button name="intent" type="submit" value="purge">
            Purge the tag
          </button>
        )}
        <button name="intent" type="submit" value="heal">
          Heal
        </button>
      </Form>
      {actionData?.purgeError !== null &&
        actionData?.purgeError !== undefined && (
          <p>
            <strong>The purge threw:</strong>{" "}
            <code>{actionData.purgeError}</code> — the invalidation path runs
            through the machinery this scene broke, so a purge that cannot be
            recorded fails loudly instead of being quietly lost.
          </p>
        )}
      <p>
        The factory has run <strong>{factoryRuns}</strong> time(s) in this
        isolate.
      </p>

      <ProbePanel probe={probe} />
      <NarrationTable narration={narration} />
    </main>
  );
}
