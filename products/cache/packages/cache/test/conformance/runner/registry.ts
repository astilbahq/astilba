import assert from "node:assert/strict";

import type { BusFrame, Registry, Store, TagKnowledge } from "@astilba/cache";
import { mirrorKeys } from "@astilba/cache/registry";
import { describe, it } from "vitest";

// Registry contract (§4.1–§4.4). The authoritative Coordinator: dual watermark
// epochs (soft never regresses hard, max-on-write — §4.1, INV-14), strictly-monotone
// epoch issuance (§4.1), `check` linearized with durable acceptance (read-your-writes
// on the primary — §4.3, E1), primary reads always authoritative/known (§4.4), and a
// monotone-high-water GC-horizon lease registration (E11, INV-28). Asserted through
// the PUBLIC `check`/`expire`/`delete`/`registerRetention` surface so a real DO
// registry client passes the same block. Frame-delivery duplicate/reorder/delay
// resilience is the subscriber's job (INV-19) and lives in the Bus contract (C0-b).
// The v0.1 FakeRegistry is the reference; deeper fake-only properties (round-trip
// lag, the GC high-water) are gated on optional harness controls.

/** Fake-only controls; a real registry client provides none of these. */
interface RegistryControls {
  /** Inject a `check` round-trip latency (the DO/primary read cost). */
  readonly setCheckLag: (ms: number) => void;
  /** Advance the deterministic clock and resolve any elapsed `check`. */
  readonly advance: (ms: number) => void;
  /** The current GC-horizon high-water (max registered retention, ms). */
  readonly maxRetentionMs: () => number;
}

export interface RegistryHarness {
  readonly registry: Registry;
  readonly controls?: RegistryControls;
  /**
   * Optional delta-batch inspector (§4.2): the replication frames the host emitted. A
   * host that mirrors its batches (the fake, and — via KV — the real Coordinator) can
   * expose them so the F12 batch-form checks run; a host without one self-skips those.
   */
  readonly emittedBatches?: () => Promise<readonly BusFrame[]>;
}

export interface RegistryContractOptions {
  readonly label?: string;
}

const TAG_A = "conformance:reg:a";
const TAG_B = "conformance:reg:b";
const UNTOUCHED = "conformance:reg:never-touched";

function known(
  entry: TagKnowledge | undefined
): Extract<TagKnowledge, { known: true }> {
  assert.ok(
    entry !== undefined && entry.known,
    "a primary read must return known knowledge for the requested tag"
  );
  return entry;
}

const checkEpochStrictlyMonotone = async (
  make: () => RegistryHarness
): Promise<void> => {
  const { registry } = make();
  const e1 = await registry.expire([TAG_A]);
  const e2 = await registry.delete([TAG_A]);
  const e3 = await registry.expire([TAG_A]);
  assert.ok(
    e2.epoch > e1.epoch && e3.epoch > e2.epoch,
    "epoch issuance must be strictly monotone across mutations"
  );
};

const checkSoftNeverRegressesHard = async (
  make: () => RegistryHarness
): Promise<void> => {
  const { registry } = make();
  const del = await registry.delete([TAG_A]);
  const exp = await registry.expire([TAG_A]);
  const knowledge = await registry.check([TAG_A]);
  const k = known(knowledge[0]);
  assert.strictEqual(
    k.hardEpoch,
    del.epoch,
    "the hard watermark must not regress after a later soft purge"
  );
  assert.strictEqual(
    k.softEpoch,
    exp.epoch,
    "the soft watermark must reflect the soft purge"
  );
  assert.ok(
    exp.epoch > del.epoch,
    "the two purges must have taken strictly increasing epochs"
  );
};

const checkMonotoneUnderInterleaving = async (
  make: () => RegistryHarness
): Promise<void> => {
  const { registry } = make();
  // Duplicate soft purges only raise the watermark (max-on-write, never regress).
  await registry.expire([TAG_A]);
  await registry.expire([TAG_A]);
  const afterSoftKnowledge = await registry.check([TAG_A]);
  const afterSoft = known(afterSoftKnowledge[0]);
  // A hard purge, then a further soft purge interleaved after it.
  await registry.delete([TAG_A]);
  await registry.expire([TAG_A]);
  const knowledge = await registry.check([TAG_A]);
  const k = known(knowledge[0]);
  assert.ok(
    k.softEpoch >= afterSoft.softEpoch,
    "the soft watermark is monotone under duplicated purges"
  );
  assert.ok(
    k.hardEpoch > 0,
    "a hard purge stays recorded after a later soft purge (dual independence)"
  );
};

const checkMaxOnWriteAcrossTags = async (
  make: () => RegistryHarness
): Promise<void> => {
  const { registry } = make();
  await registry.expire([TAG_A, TAG_B]);
  await registry.delete([TAG_A]);
  const knowledge = await registry.check([TAG_A, TAG_B]);
  const a = known(knowledge[0]);
  const b = known(knowledge[1]);
  assert.ok(a.hardEpoch > 0, "the hard-purged tag carries a hard watermark");
  assert.strictEqual(
    b.hardEpoch,
    0,
    "an unrelated tag is unaffected by another tag's hard purge (independence)"
  );
};

const checkCheckLinearizes = async (
  make: () => RegistryHarness
): Promise<void> => {
  const { registry } = make();
  const ack = await registry.expire([TAG_A]);
  const knowledge = await registry.check([TAG_A]);
  const k = known(knowledge[0]);
  assert.ok(
    k.throughEpoch >= ack.epoch,
    "check must observe every durably-accepted epoch (read-your-writes)"
  );
  assert.ok(
    k.softEpoch >= ack.epoch,
    "the just-accepted soft purge must be visible to the next check"
  );
};

const checkPrimaryReadsKnown = async (
  make: () => RegistryHarness
): Promise<void> => {
  const { registry } = make();
  const knowledge = await registry.check([UNTOUCHED]);
  const k = known(knowledge[0]);
  assert.strictEqual(
    k.softEpoch,
    0,
    "an untouched tag reads as a known zero watermark, never as unknown"
  );
  assert.strictEqual(k.hardEpoch, 0, "an untouched tag has no hard watermark");
};

const checkRetentionMonotone = async (
  make: () => RegistryHarness
): Promise<void> => {
  const harness = make();
  const horizon = harness.controls?.maxRetentionMs;
  if (!horizon) {
    // No horizon accessor (real driver) — the monotone high-water is verified at the
    // DO thin-shell conformance (C1) and in sacred INV-16/INV-28. Registration must
    // at least accept churn without throwing.
    await harness.registry.registerRetention(1000);
    await harness.registry.registerRetention(2000);
    return;
  }
  await harness.registry.registerRetention(1000);
  assert.strictEqual(horizon(), 1000, "the horizon tracks the first lease");
  await harness.registry.registerRetention(5000);
  assert.strictEqual(horizon(), 5000, "a larger lease raises the horizon");
  await harness.registry.registerRetention(2000);
  assert.strictEqual(
    horizon(),
    5000,
    "a smaller lease never retracts the monotone high-water horizon (E11)"
  );
};

const checkAuthoritativeUnderLag = async (
  make: () => RegistryHarness
): Promise<void> => {
  const harness = make();
  const { controls } = harness;
  if (!controls) {
    return;
  }
  controls.setCheckLag(5000);
  await harness.registry.expire([TAG_A]);
  const pending = harness.registry.check([TAG_A]);
  // A hard purge lands DURING the round-trip; a primary read is authoritative and
  // must reflect it (§4.4 — only completion is delayed, not the answer).
  await harness.registry.delete([TAG_A]);
  controls.advance(5000);
  const resolved = await pending;
  const k = known(resolved[0]);
  assert.ok(
    k.hardEpoch > 0,
    "a primary read reflects a mutation that landed during its round-trip"
  );
};

/** The E19 `regId` charset/length (`[a-z0-9._-]`, 1–64). */
const REG_ID_PATTERN = /^[a-z0-9._-]{1,64}$/u;

const checkRegIdPresentAndScoped = async (
  make: () => RegistryHarness
): Promise<void> => {
  const { registry } = make();
  // Presence: `regId` is on the Registry interface (the channel the reader scopes its keyspace
  // and asserts the pointer echo against — E19). Charset: it must be an injective-safe id (the
  // `:` delimiter is outside the charset, so a scoped key parses unambiguously, no escaping).
  assert.strictEqual(
    typeof registry.regId,
    "string",
    "a registry must expose a string regId (the E19 scoping channel)"
  );
  assert.ok(
    REG_ID_PATTERN.test(registry.regId),
    `regId must match [a-z0-9._-], length 1-64 (E19); got ${JSON.stringify(registry.regId)}`
  );
};

const checkNoAxisEmptyChange = async (
  make: () => RegistryHarness
): Promise<void> => {
  const harness = make();
  const emitted = harness.emittedBatches;
  if (!emitted) {
    // No batch inspector (a host that doesn't mirror its frames) — self-skip, like the
    // control-gated checks. The F12 batch form is proven wherever batches ARE inspectable
    // (the fake reference here, and the real Coordinator's KV mirror in the C1 transcript).
    return;
  }
  // Emit a soft then a hard batch so there is real content to inspect.
  await harness.registry.expire([TAG_A]);
  await harness.registry.delete([TAG_B]);
  const frames = await emitted();
  let inspected = 0;
  for (const frame of frames) {
    for (const change of frame.changes) {
      inspected += 1;
      // F12 addendum: a change carrying NEITHER axis is illegal — an axis-empty change
      // asserts nothing, so it must never appear in a batch.
      assert.ok(
        change.softEpoch !== undefined || change.hardEpoch !== undefined,
        "a batch change must carry a soft or hard axis; an axis-empty change is illegal (F12)"
      );
    }
  }
  assert.ok(
    inspected > 0,
    "the purges must have emitted at least one change to inspect (non-vacuity)"
  );
};

// ── Isolation property (E19) — two registries, one store, no cross-observation ──────────

/**
 * A shared-store isolation harness: `writeUnder(regId)` drives one registry's mirror writes for
 * `regId` into the SHARED `store`. The reference scopes every key through `mirrorKeys(regId)`; a
 * prefix-dropping impl collides them onto one unscoped key. The check reads the store back at the
 * scoped keys and proves each registry blessed only its OWN keyspace (E19).
 */
export interface IsolationHarness {
  readonly store: Store;
  writeUnder(regId: string): Promise<void>;
}

/** Parse a stored `state` pointer's echoed `regId`, or undefined if absent/malformed. */
async function readEchoedRegId(
  store: Store,
  key: string
): Promise<string | undefined> {
  const stored = await store.get(key);
  if (!stored) {
    return undefined;
  }
  const parsed: unknown = JSON.parse(stored.value);
  if (typeof parsed !== "object" || parsed === null || !("regId" in parsed)) {
    return undefined;
  }
  const { regId } = parsed;
  return typeof regId === "string" ? regId : undefined;
}

const ISO_A = "iso-a";
const ISO_B = "iso-b";

const checkIsolationScoped = async (
  make: () => IsolationHarness
): Promise<void> => {
  const h = make();
  // Two registries, DIFFERENT regIds, ONE store — the E19 collision scenario.
  await h.writeUnder(ISO_A);
  await h.writeUnder(ISO_B);
  const aKeys = mirrorKeys(ISO_A);
  const bKeys = mirrorKeys(ISO_B);

  // Each registry blessed its OWN scoped pointer, echoing its OWN regId. A prefix-dropping writer
  // collides both onto one unscoped key, so A's SCOPED key is empty (and B's clobbered A's) —
  // either failure trips an assertion here.
  assert.strictEqual(
    await readEchoedRegId(h.store, aKeys.stateKey),
    ISO_A,
    "registry A must bless its OWN scoped state pointer, echoing its regId"
  );
  assert.strictEqual(
    await readEchoedRegId(h.store, bKeys.stateKey),
    ISO_B,
    "registry B must bless its OWN scoped state pointer, echoing its regId"
  );

  // No cross-observation: A's first delta batch lives under A's scope, B's under B's — and neither
  // wrote a SECOND batch (one purge each), so A's `delta:2` is absent (the restored absence check).
  assert.ok(
    (await h.store.get(aKeys.deltaKey(1))) !== undefined,
    "A's own first delta batch is present under A's scope"
  );
  assert.ok(
    (await h.store.get(bKeys.deltaKey(1))) !== undefined,
    "B's own first delta batch is present under B's scope"
  );
  assert.strictEqual(
    await h.store.get(aKeys.deltaKey(2)),
    undefined,
    "A wrote no second delta batch — its keyspace is not polluted by B (absence)"
  );
};

type RegistryCheck = (make: () => RegistryHarness) => Promise<void>;

/** The individually-callable Registry checks (each throws on violation). */
export const registryChecks: {
  readonly authoritativeUnderLag: RegistryCheck;
  readonly checkLinearizes: RegistryCheck;
  readonly epochStrictlyMonotone: RegistryCheck;
  readonly isolationScoped: (make: () => IsolationHarness) => Promise<void>;
  readonly maxOnWriteAcrossTags: RegistryCheck;
  readonly monotoneUnderInterleaving: RegistryCheck;
  readonly noAxisEmptyChange: RegistryCheck;
  readonly primaryReadsKnown: RegistryCheck;
  readonly regIdPresentAndScoped: RegistryCheck;
  readonly retentionMonotone: RegistryCheck;
  readonly softNeverRegressesHard: RegistryCheck;
} = {
  authoritativeUnderLag: checkAuthoritativeUnderLag,
  checkLinearizes: checkCheckLinearizes,
  epochStrictlyMonotone: checkEpochStrictlyMonotone,
  isolationScoped: checkIsolationScoped,
  maxOnWriteAcrossTags: checkMaxOnWriteAcrossTags,
  monotoneUnderInterleaving: checkMonotoneUnderInterleaving,
  noAxisEmptyChange: checkNoAxisEmptyChange,
  primaryReadsKnown: checkPrimaryReadsKnown,
  regIdPresentAndScoped: checkRegIdPresentAndScoped,
  retentionMonotone: checkRetentionMonotone,
  softNeverRegressesHard: checkSoftNeverRegressesHard,
};

/** Register the Registry conformance suite for a provided implementation factory. */
export function runRegistryContract(
  make: () => RegistryHarness,
  opts: RegistryContractOptions = {}
): void {
  const probe = make();
  const hasLag = probe.controls !== undefined;
  const hasBatches = probe.emittedBatches !== undefined;
  describe(`${opts.label ?? "Registry"} — conformance contract`, () => {
    it("issues strictly monotone epochs (§4.1)", async () => {
      await checkEpochStrictlyMonotone(make);
    });
    it("soft never regresses hard; watermarks are max-on-write and independent (§4.1, INV-14)", async () => {
      await checkSoftNeverRegressesHard(make);
    });
    it("keeps watermarks monotone under duplicated and interleaved purges (§4.1)", async () => {
      await checkMonotoneUnderInterleaving(make);
    });
    it("bumps only the purged tag's watermark, per-tag independent (§4.1)", async () => {
      await checkMaxOnWriteAcrossTags(make);
    });
    it("linearizes check with durable acceptance — read-your-writes on the primary (§4.3, E1)", async () => {
      await checkCheckLinearizes(make);
    });
    it("returns known/authoritative knowledge for every tag — unknown is a replica concept (§4.4)", async () => {
      await checkPrimaryReadsKnown(make);
    });
    it("registers a GC-horizon lease as a monotone high-water, accepting churn (E11, INV-28)", async () => {
      await checkRetentionMonotone(make);
    });
    it("exposes a charset-valid regId — the E19 mirror-key scoping channel (§4.2)", async () => {
      await checkRegIdPresentAndScoped(make);
    });
    if (hasBatches) {
      it("emits no axis-empty change — every batch change carries a soft or hard axis (F12)", async () => {
        await checkNoAxisEmptyChange(make);
      });
    }
    if (hasLag) {
      it("resolves a primary read against state that mutated during its round-trip (§4.4)", async () => {
        await checkAuthoritativeUnderLag(make);
      });
    }
  });
}
