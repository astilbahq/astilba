// The scene script's tests — the demo's machinery.
//
// The load-bearing one is the mirror-keyspace test. The registry-outage scene has to make the
// replication mirror unreadable, which means it has to know which keys the mirror lives under — and
// that grammar belongs to the library, not to this demo. If it ever changes, a scene that had
// hard-coded the old shape would go on "passing" while faulting NOTHING: the console would show a
// Coordinator outage that the cache sailed through, which is the most dishonest picture this demo
// could paint and would look exactly like the product's best moment.
//
// So the scene derives its prefix from the library's own key formatter, and this file checks that
// derivation against the formatter's OTHER outputs — the delta and snapshot keys the derivation
// never sees. That is what makes it a check rather than a restatement of the same assumption.

import { mirrorKeys } from "@astilba/cache/registry";
import { describe, expect, it } from "vitest";

import { noFaults } from "./dials";
import type { SceneId } from "./script";
import { isSceneId, SCENE_IDS, sceneOf } from "./script";

const REGISTRY_NAME = "demo";

/** Arm one scene's fault straight off the catalogue. The EPISODE-level transition (heal, mint,
 * stopwatch) is `armScene`'s, and it is toothed beside `app/world.ts`; what is on trial here is the
 * catalogue's own claim — that each scene touches exactly the dials it says it does. */
function armed(id: SceneId) {
  const dials = noFaults();
  sceneOf(id).arm(dials, { registryName: REGISTRY_NAME });
  return dials;
}

describe("the scene catalogue", () => {
  it("names the three faults the demo exists to show", () => {
    expect([...SCENE_IDS]).toStrictEqual([
      "backend-kill",
      "bus-drop",
      "registry-outage",
    ]);
  });

  it("recognises its own ids and nothing else", () => {
    expect(isSceneId("bus-drop")).toBe(true);
    expect(isSceneId("../../etc/passwd")).toBe(false);
    expect(isSceneId("")).toBe(false);
  });

  it("gives every scene something to say about what MAY follow", () => {
    for (const id of SCENE_IDS) {
      const scene = sceneOf(id);
      expect(scene.mayHappen.length).toBeGreaterThan(0);
      expect(scene.injects).not.toBe("");
      expect(scene.premise).not.toBe("");
    }
  });
});

describe("backend-kill", () => {
  it("kills reads and writes, and touches nothing else", () => {
    const dials = armed("backend-kill");
    expect(dials.kv.failReads).toBe(true);
    expect(dials.kv.failWrites).toBe(true);
    expect(dials.bus.dialRefused).toBe(false);
    expect(dials.kv.unreadablePrefix).toBeNull();
  });
});

describe("bus-drop", () => {
  it("refuses the dial and leaves the store completely alone", () => {
    const dials = armed("bus-drop");
    expect(dials.bus.dialRefused).toBe(true);
    expect(dials.kv.failReads).toBe(false);
    expect(dials.kv.failWrites).toBe(false);
    expect(dials.kv.unreadablePrefix).toBeNull();
  });
});

describe("registry-outage", () => {
  it("refuses the dial AND makes the mirror unreadable — both halves, or it is a different scene", () => {
    const dials = armed("registry-outage");
    expect(dials.bus.dialRefused).toBe(true);
    expect(dials.kv.unreadablePrefix).toBeDefined();
  });

  it("makes the REAL mirror keys unreadable — every one the library's own formatter can mint", () => {
    // The anti-drift tooth. `mirrorPrefix` derives from the pointer key alone; these are the delta
    // and snapshot keys it never looks at. If the mirror's grammar ever moves, this fails here
    // rather than silently on screen.
    const dials = armed("registry-outage");
    const prefix = dials.kv.unreadablePrefix;
    expect(prefix).toBeDefined();
    const keys = mirrorKeys(REGISTRY_NAME);
    for (const key of [keys.stateKey, keys.deltaKey(7), keys.snapKey(3)]) {
      expect(
        key.startsWith(prefix ?? "\u0000"),
        `the mirror key ${key} must fall inside the unreadable keyspace ${String(prefix)} — a scene that faults nothing looks exactly like a system that shrugged`
      ).toBe(true);
    }
  });

  it("leaves the cache's OWN entries readable — that is what makes it a REGISTRY outage", () => {
    const dials = armed("registry-outage");
    const prefix = dials.kv.unreadablePrefix ?? "\u0000";
    expect("demo:headline".startsWith(prefix)).toBe(false);
    expect(dials.kv.failReads).toBe(false);
  });

  it("scopes the outage to THIS registry, not to every registry sharing the namespace", () => {
    const dials = armed("registry-outage");
    const prefix = dials.kv.unreadablePrefix ?? "\u0000";
    expect(mirrorKeys("someone-else").stateKey.startsWith(prefix)).toBe(false);
  });
});

// ── What each scene INSTRUCTS ───────────────────────────────────────────────
//
// A console button is an instruction whether or not a bullet mentions it, so what a scene offers is
// part of what a scene claims — and therefore part of what is on trial.
describe("the flows each scene instructs", () => {
  it("does NOT instruct a purge on the flagship", () => {
    // THE CUT. The purge-under-outage flow is not part of this scene's argument and is not offered
    // on its page. MUTATION: give `backend-kill` a `purgeInstruction` and this goes red.
    expect(
      sceneOf("backend-kill").purgeInstruction,
      "the flagship is offering a flow it does not narrate"
    ).toBeNull();
  });

  it("instructs a purge exactly where the purge IS the argument", () => {
    for (const id of ["bus-drop", "registry-outage"] as const) {
      expect(
        sceneOf(id).purgeInstruction,
        `${id}'s whole point is what a purge does under its fault, and it offers no instruction for one`
      ).not.toBeNull();
    }
  });

  it("boots an episode reader ONLY for the scene whose subject is a reader's first moments", () => {
    // The rig is scoped, and scoping it is what keeps it honest: everywhere else, the reader the
    // page narrates is the reader the app actually runs. MUTATION: flip `bootsOwnReader` on any
    // other scene and this goes red.
    expect(sceneOf("bus-drop").bootsOwnReader).toBe(true);
    expect(sceneOf("backend-kill").bootsOwnReader).toBe(false);
    expect(sceneOf("registry-outage").bootsOwnReader).toBe(false);
  });

  it("NAMES the rig in the copy wherever it uses one — an observation states its apparatus", () => {
    // A fresh reader booted under a dead dial is not a fabrication; it is an apparatus. It stops
    // being honest the moment the page shows the result without saying how it was produced.
    const deaf = sceneOf("bus-drop");
    expect(deaf.bootsOwnReader).toBe(true);
    expect(
      deaf.mayHappen.some((line) => /RIG, STATED/u.test(line)),
      "the scene mints its own reader and never tells the viewer it did"
    ).toBe(true);
  });
});
