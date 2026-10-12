import assert from "node:assert/strict";

import { isStoreWriteError } from "@astilba/cache";
import type { CasStore, Codec, Store } from "@astilba/cache";
import { describe, it } from "vitest";

// The read half of the failure contract is kernel-internal (not on the barrel), so it is imported by
// module path — the same door the drivers that raise it use.
import { isStoreReadError } from "../../../src/drivers";

// Store contract (§8, §5.0). A parameterized behavior-spec any KV-like driver must
// satisfy: byte-stable round-trip through the codec (INV-8), same-view
// read-your-own-write (the RCW floor the born-monotone write-back needs — §5.0, Q6),
// TTL expiry on a deterministic timeline (§8), and an optional compare-and-set
// capability whose adversarial interleaving must not regress the stored
// (bornEpoch, fence) ordering (E3). The v0.1 FakeStore is the reference and passes
// first; a real KV / Redis driver passes the same block later. Every fake-vs-real
// gap (KV's 60s expirationTtl floor, the CAS-less one-cycle RCW race) is recorded in
// DIVERGENCES.md, never asserted away.
//
// D3 migration: the `CasStore` capability type (with `CasOrder` / `CasRecord`) now
// lives in `@astilba/cache` (drivers.ts) as an exported capability the first real CAS
// driver will implement — a TYPE MOVE only; the kernel's floor stays read-compare-write
// (INV-17), prefer-CAS consumption deferred. This layer imports it from the package.

/** What a Store implementation factory returns for the contract to drive. */
export interface StoreHarness {
  readonly store: Store;
  /** Present only for a CAS-capable driver (real Redis/DO); absent for FakeStore. */
  readonly cas?: CasStore;
  /** Deterministic time control (fakes). Absent ⇒ TTL-expiry is left to integration. */
  readonly advance?: (ms: number) => void;
  /**
   * Arms the next write to be throttled (fakes). Absent ⇒ the throttle check self-skips:
   * a real KV driver under Miniflare cannot be forced to emit a real 429 (D2), so its
   * classified-signaling is an integration/probe-lane concern (lane law).
   */
  readonly forceThrottle?: () => void;
  /**
   * Arms the next READ to fail the way this driver's own tier fails. Absent ⇒ the read-
   * classification check self-skips, on the same terms as `forceThrottle`: a fault a harness cannot
   * induce is not a fault this deterministic lane can judge.
   *
   * The v0.1 FakeStore harness deliberately offers NO read fault hand. Its injected read failure is
   * UNCLASSIFIED by design — it is the subject of the kernel's rethrow path, the one branch that
   * must never be absorbed — so pointing this check at it would demand the fake stop being the thing
   * the sacred suite needs it to be.
   */
  readonly forceReadFailure?: () => void;
}

export interface StoreContractOptions {
  readonly label?: string;
  readonly codec?: Codec;
  readonly capabilities?: { readonly cas?: boolean };
  /** The impl's documented min residency in seconds (FakeStore 0; KV 60). Default 60. */
  readonly ttlFloorSeconds?: number;
  /** The impl's max value size in bytes. Absent ⇒ the oversize check self-skips. */
  readonly maxValueBytes?: number;
}

// KV's minimum `expirationTtl` (§8 driver table). The TTL-expiry assertion uses this
// floor so a real KV driver — which silently raises any smaller TTL to 60s — passes
// the same check the fake does (the sub-floor divergence is ledger-recorded, D1).
const KV_TTL_FLOOR_SECONDS = 60;
const RT_KEY = "conformance:store:roundtrip";
const RCW_KEY = "conformance:store:rcw";
const TTL_KEY = "conformance:store:ttl";
const CAS_KEY = "conformance:store:cas";
const THROTTLE_KEY = "conformance:store:throttle";
const OVERSIZE_KEY = "conformance:store:oversize";
const READ_FAIL_KEY = "conformance:store:read-failure";

// A self-contained byte-stable JSON reference codec, used when `opts.codec` is unset.
const jsonCodec: Codec = {
  decode: (value: string): unknown => JSON.parse(value) as unknown,
  encode: (value: unknown): string => JSON.stringify(value),
  id: "conformance-json/1",
};

const roundTripValues: readonly unknown[] = [
  { a: 1, b: [2, 3], c: "x" },
  "plain-string",
  42,
  true,
  [],
  { nested: { deep: [1, 2, 3] } },
];

const checkRoundTrip = async (
  make: () => StoreHarness,
  opts: StoreContractOptions = {}
): Promise<void> => {
  const codec = opts.codec ?? jsonCodec;
  const { store } = make();
  for (const value of roundTripValues) {
    const bytes = codec.encode(value);
    await store.set(RT_KEY, bytes);
    const read = await store.get(RT_KEY);
    assert.ok(read !== undefined, "round-trip: value must read back after set");
    assert.strictEqual(
      read.value,
      bytes,
      "round-trip: stored bytes must be byte-identical"
    );
    assert.deepStrictEqual(
      codec.decode(read.value),
      value,
      "round-trip: decode must reproduce the encoded value"
    );
  }
};

const checkReadYourOwnWrite = async (
  make: () => StoreHarness,
  opts: StoreContractOptions = {}
): Promise<void> => {
  const codec = opts.codec ?? jsonCodec;
  const { store } = make();
  const bytes = codec.encode({ v: "rcw" });
  await store.set(RCW_KEY, bytes);
  const read = await store.get(RCW_KEY);
  assert.ok(
    read !== undefined,
    "RCW: a same-view read after a write must observe that write"
  );
  assert.strictEqual(
    read.value,
    bytes,
    "RCW: same-view read must return the just-written bytes"
  );
};

/* References: §4.2. */

const checkReadHintTolerated = async (
  make: () => StoreHarness,
  opts: StoreContractOptions = {}
): Promise<void> => {
  const codec = opts.codec ?? jsonCodec;
  const { store } = make();
  const bytes = codec.encode({ v: "hint" });
  await store.set(RCW_KEY, bytes);
  const hinted = await store.get(RCW_KEY, "pointer");
  const unhinted = await store.get(RCW_KEY);
  assert.strictEqual(
    hinted?.value,
    unhinted?.value,
    "read hint: the readKind hint must not change the value a Store returns"
  );
  assert.strictEqual(
    hinted?.value,
    bytes,
    "read hint: the value still round-trips under a hint"
  );
};

const checkTtlExpiry = async (
  make: () => StoreHarness,
  opts: StoreContractOptions = {}
): Promise<void> => {
  const harness = make();
  if (!harness.advance) {
    // No deterministic time control (real driver, conformance lane) — TTL expiry is
    // an integration-lane assertion (lane law). Nothing to assert deterministically.
    return;
  }
  const floorSeconds = opts.ttlFloorSeconds ?? KV_TTL_FLOOR_SECONDS;
  await harness.store.set(TTL_KEY, "residency", {
    expirationTtl: floorSeconds,
  });
  const before = await harness.store.get(TTL_KEY);
  assert.ok(before !== undefined, "TTL: value must be present before expiry");
  harness.advance(floorSeconds * 1000 + 1);
  const after = await harness.store.get(TTL_KEY);
  assert.strictEqual(
    after,
    undefined,
    "TTL: value must be gone once its residency elapses"
  );
};

const checkCasNoRegress = async (
  make: () => StoreHarness,
  _opts: StoreContractOptions = {}
): Promise<void> => {
  const { cas } = make();
  assert.ok(
    cas !== undefined,
    "cas capability declared but the harness exposes no CasStore"
  );
  await cas.seed(CAS_KEY, { epoch: 0, fence: 0 });
  const racerA = await cas.load(CAS_KEY);
  const racerB = await cas.load(CAS_KEY);
  assert.ok(
    racerA !== undefined && racerB !== undefined,
    "cas: the seeded record must load for both racers"
  );
  // Both racers read the same version. The higher-ordered write commits first.
  const won = await cas.swap(CAS_KEY, racerA.token, { epoch: 1, fence: 5 });
  assert.ok(
    won !== undefined,
    "cas: the first writer holding a live token must commit"
  );
  // The straggler's token is now stale. An atomic CAS must reject it, so the stored
  // (epoch, fence) can never regress to the lower-fenced write.
  const lost = await cas.swap(CAS_KEY, racerB.token, { epoch: 1, fence: 2 });
  assert.strictEqual(
    lost,
    undefined,
    "cas: a stale-token write must be rejected, not applied"
  );
  const final = await cas.load(CAS_KEY);
  assert.ok(final !== undefined, "cas: the record must remain after the race");
  assert.strictEqual(
    final.order.fence,
    5,
    "cas: the stored fence must not regress below the committed maximum"
  );
};

const checkThrottleClassified = async (
  make: () => StoreHarness,
  opts: StoreContractOptions = {}
): Promise<void> => {
  const harness = make();
  if (!harness.forceThrottle) {
    // No throttle control (a real KV driver can't be forced to a real 429 — D2). The
    // classified-signaling contract is an integration/probe concern for such a driver;
    // nothing to assert deterministically here (lane law).
    return;
  }
  const codec = opts.codec ?? jsonCodec;
  harness.forceThrottle();
  // A throttled write must REJECT, and the rejection must carry the STRUCTURAL
  // classification (§8, E13): `isStoreWriteError` (never `instanceof`), code `throttled`,
  // retryable. A driver that throttles with an unclassified error fails the predicate.
  await assert.rejects(
    harness.store.set(THROTTLE_KEY, codec.encode({ v: "throttled" })),
    (error: unknown): boolean =>
      isStoreWriteError(error) && error.code === "throttled" && error.retryable,
    "throttle: a throttled write must reject with a classified StoreWriteError {code:'throttled', retryable:true} — an unclassified error must fail this"
  );
};

const checkReadFailureClassified = async (
  make: () => StoreHarness,
  opts: StoreContractOptions = {}
): Promise<void> => {
  const harness = make();
  if (!harness.forceReadFailure) {
    // No read fault hand (the v0.1 fake by design, a real KV driver under Miniflare by platform).
    // Nothing to induce, so nothing to judge deterministically here (lane law).
    return;
  }
  const codec = opts.codec ?? jsonCodec;
  await harness.store.set(READ_FAIL_KEY, codec.encode({ v: "read" }));
  harness.forceReadFailure();
  // The obligation this clause exists to hand every future driver at birth: a failed READ must
  // reject in the read contract's own vocabulary (§8, E13) — structurally (`isStoreReadError`, never
  // `instanceof`), code `throttled` or `unavailable`. It belongs to the DRIVER because platform
  // knowledge does: only the shell over a tier knows what that tier's faults look like. The kernel's
  // half is the opposite duty — it absorbs what a driver named and rethrows what nobody named, so an
  // unclassified rejection is a non-conformance signal rather than a silent miss. A driver that
  // classifies nothing therefore does not inherit the kernel's tolerance; it inherits its alarm, on
  // every real read failure. That is what this rejects.
  await assert.rejects(
    harness.store.get(READ_FAIL_KEY),
    (error: unknown): boolean => isStoreReadError(error),
    "read failure: a failed read must reject with a classified StoreReadError {code:'throttled'|'unavailable'} — an unclassified error must fail this"
  );
};

const checkOversizeClassified = async (
  make: () => StoreHarness,
  opts: StoreContractOptions = {}
): Promise<void> => {
  const { maxValueBytes } = opts;
  if (maxValueBytes === undefined) {
    // The impl declares no max value size — nothing to over-fill. Self-skip.
    return;
  }
  const codec = opts.codec ?? jsonCodec;
  const { store } = make();
  // Seed a known in-bounds value so the truncation assertion has something to protect.
  const prior = codec.encode({ v: "prior" });
  await store.set(OVERSIZE_KEY, prior);
  // One byte over the ceiling (ASCII ⇒ bytes === length).
  const oversize = "x".repeat(maxValueBytes + 1);
  // An over-limit write must REJECT with the structural classification (§8, E13): code
  // `too_large`, NOT retryable — never a silent truncate-and-resolve.
  await assert.rejects(
    store.set(OVERSIZE_KEY, oversize),
    (error: unknown): boolean =>
      isStoreWriteError(error) &&
      error.code === "too_large" &&
      !error.retryable,
    "oversize: an over-limit write must reject with a classified StoreWriteError {code:'too_large', retryable:false} — never truncate"
  );
  // Non-vacuity (never-truncate): the rejected write must not have landed. The prior
  // in-bounds value must read back byte-identical — a truncating driver would have
  // clobbered it with a shortened payload.
  const after = await store.get(OVERSIZE_KEY);
  assert.ok(
    after !== undefined,
    "oversize: the prior in-bounds value must survive a rejected oversize write"
  );
  assert.strictEqual(
    after.value,
    prior,
    "oversize: a rejected oversize write must not truncate or overwrite the stored value"
  );
};

type StoreCheck = (
  make: () => StoreHarness,
  opts?: StoreContractOptions
) => Promise<void>;

/** The individually-callable Store checks (each throws on violation). */
export const storeChecks: {
  readonly casNoRegress: StoreCheck;
  readonly oversizeClassified: StoreCheck;
  readonly readFailureClassified: StoreCheck;
  readonly readHintTolerated: StoreCheck;
  readonly readYourOwnWrite: StoreCheck;
  readonly roundTrip: StoreCheck;
  readonly throttleClassified: StoreCheck;
  readonly ttlExpiry: StoreCheck;
} = {
  casNoRegress: checkCasNoRegress,
  oversizeClassified: checkOversizeClassified,
  readFailureClassified: checkReadFailureClassified,
  readHintTolerated: checkReadHintTolerated,
  readYourOwnWrite: checkReadYourOwnWrite,
  roundTrip: checkRoundTrip,
  throttleClassified: checkThrottleClassified,
  ttlExpiry: checkTtlExpiry,
};

/** Register the Store conformance suite for a provided implementation factory. */
export function runStoreContract(
  make: () => StoreHarness,
  opts: StoreContractOptions = {}
): void {
  describe(`${opts.label ?? "Store"} — conformance contract`, () => {
    it("round-trips values byte-stable through the codec (§8, INV-8)", async () => {
      await checkRoundTrip(make, opts);
    });
    it("serves a same-view read-your-own-write (§5.0, Q6)", async () => {
      await checkReadYourOwnWrite(make, opts);
    });
    it("tolerates a readKind read-cache hint transparently (§4.2, Ruling 2)", async () => {
      await checkReadHintTolerated(make, opts);
    });
    it("expires a value once its residency elapses on a deterministic timeline (§8)", async () => {
      await checkTtlExpiry(make, opts);
    });
    it("rejects a throttled write with a classified 'throttled' signal, never a bare error (§8, E13)", async () => {
      await checkThrottleClassified(make, opts);
    });
    it("rejects an oversize value with a classified 'too_large' signal and never truncates (§8, E13)", async () => {
      await checkOversizeClassified(make, opts);
    });
    it("rejects a failed read with a classified StoreReadError, never a bare error (§8, E13)", async () => {
      await checkReadFailureClassified(make, opts);
    });
    if (opts.capabilities?.cas === true) {
      it("keeps atomic CAS from regressing (bornEpoch, fence) under an adversarial interleaving (§5.0, E3)", async () => {
        await checkCasNoRegress(make, opts);
      });
    } else {
      it("exposes no CAS — the one-cycle read-compare-write race is documented, not asserted away (E3; DIVERGENCES D3)", () => {
        // FakeStore has no compare-and-set; the kernel's KV path is read-compare-write
        // whose residual race is bounded to one refresh cycle (§5.0). The honest
        // no-CAS condition is asserted; the race itself is ledger-recorded (D3).
        assert.strictEqual(
          make().cas,
          undefined,
          "no-CAS branch: the harness must expose no compare-and-set"
        );
      });
    }
  });
}
