import { describe, expect, it } from "vitest";

import {
  makeAxisEmptyBatchRegistryHarness,
  makeForgetfulBusHarness,
  makeNonAtomicCasHarness,
  makeNonByteStableCodec,
  makeNonMonotoneLockHarness,
  makePrefixDroppingMirrorHarness,
  makeRcwLosingStoreHarness,
  makeSilentBusHarness,
  makeSoftRegressesHardRegistryHarness,
  makeTruncatingOversizeStoreHarness,
  makeUnclassifiedThrottleStoreHarness,
  makeUnderdeclaringBusHarness,
  makeUnderdeclaringGapBusHarness,
} from "./fixtures/broken";
import {
  makeDoBusHarness,
  makeNoCloseOnDoubtDoBusHarness,
  makeNoEchoCheckDoBusHarness,
} from "./fixtures/do-bus";
import {
  makeCasStoreHarness,
  makeFakeBusHarness,
  makeFakeLockHarness,
  makeFakeRegistryHarness,
  makeFakeStoreHarness,
  makeScopedMirrorHarness,
  referenceCodec,
} from "./fixtures/reference";
import { busChecks } from "./runner/bus";
import { codecChecks } from "./runner/codec";
import { lockChecks } from "./runner/lock";
import { registryChecks } from "./runner/registry";
import { storeChecks } from "./runner/store";

const OVERSIZE_LIMIT = 64;

describe("conformance teeth — every contract discriminates a wrong driver", () => {
  it("Store RCW check REJECTS a store that loses read-your-own-write", async () => {
    await expect(
      storeChecks.readYourOwnWrite(makeRcwLosingStoreHarness(), {
        codec: referenceCodec,
      })
    ).rejects.toThrow();
  });
  it("Store RCW check ACCEPTS the reference (non-vacuity)", async () => {
    await expect(
      storeChecks.readYourOwnWrite(makeFakeStoreHarness(), {
        codec: referenceCodec,
      })
    ).resolves.toBeUndefined();
  });

  it("Store throttle check REJECTS a store that throttles with an unclassified error", async () => {
    await expect(
      storeChecks.throttleClassified(makeUnclassifiedThrottleStoreHarness(), {
        codec: referenceCodec,
      })
    ).rejects.toThrow();
  });
  it("Store throttle check ACCEPTS the classified reference (non-vacuity)", async () => {
    await expect(
      storeChecks.throttleClassified(makeFakeStoreHarness(), {
        codec: referenceCodec,
      })
    ).resolves.toBeUndefined();
  });

  it("Store oversize check REJECTS a store that silently truncates an oversize value", async () => {
    await expect(
      storeChecks.oversizeClassified(
        makeTruncatingOversizeStoreHarness(OVERSIZE_LIMIT),
        { maxValueBytes: OVERSIZE_LIMIT }
      )
    ).rejects.toThrow();
  });
  it("Store oversize check ACCEPTS the typed reference (non-vacuity)", async () => {
    await expect(
      storeChecks.oversizeClassified(makeFakeStoreHarness(0, OVERSIZE_LIMIT), {
        maxValueBytes: OVERSIZE_LIMIT,
      })
    ).resolves.toBeUndefined();
  });

  it("Store CAS check REJECTS a store with non-atomic compare-and-set", async () => {
    await expect(
      storeChecks.casNoRegress(makeNonAtomicCasHarness(), {
        capabilities: { cas: true },
      })
    ).rejects.toThrow();
  });
  it("Store CAS check ACCEPTS an atomic reference (non-vacuity)", async () => {
    await expect(
      storeChecks.casNoRegress(makeCasStoreHarness(), {
        capabilities: { cas: true },
      })
    ).resolves.toBeUndefined();
  });

  it("Registry dual-independence check REJECTS a registry where soft regresses hard", async () => {
    await expect(
      registryChecks.softNeverRegressesHard(
        makeSoftRegressesHardRegistryHarness()
      )
    ).rejects.toThrow();
  });
  it("Registry dual-independence check ACCEPTS the reference (non-vacuity)", async () => {
    await expect(
      registryChecks.softNeverRegressesHard(makeFakeRegistryHarness())
    ).resolves.toBeUndefined();
  });

  it("Registry axis-empty check REJECTS a registry emitting an axis-empty batch change", async () => {
    await expect(
      registryChecks.noAxisEmptyChange(makeAxisEmptyBatchRegistryHarness())
    ).rejects.toThrow();
  });
  it("Registry axis-empty check ACCEPTS the reference (non-vacuity)", async () => {
    await expect(
      registryChecks.noAxisEmptyChange(makeFakeRegistryHarness())
    ).resolves.toBeUndefined();
  });

  it("Registry isolation check REJECTS a prefix-dropping mirror sink (unscoped __state — E19)", async () => {
    await expect(
      registryChecks.isolationScoped(makePrefixDroppingMirrorHarness())
    ).rejects.toThrow();
  });
  it("Registry isolation check ACCEPTS the scoped reference (non-vacuity — E19)", async () => {
    await expect(
      registryChecks.isolationScoped(makeScopedMirrorHarness())
    ).resolves.toBeUndefined();
  });

  it("Codec round-trip check REJECTS a non-byte-stable codec", async () => {
    await expect(
      codecChecks.roundTrip(makeNonByteStableCodec())
    ).rejects.toThrow();
  });
  it("Codec round-trip check ACCEPTS the reference (non-vacuity)", async () => {
    await expect(
      codecChecks.roundTrip(() => referenceCodec)
    ).resolves.toBeUndefined();
  });

  it("Lock fence check REJECTS a lock with non-monotone fences", async () => {
    await expect(
      lockChecks.monotonePerKey(makeNonMonotoneLockHarness())
    ).rejects.toThrow();
  });
  it("Lock fence check ACCEPTS the reference (non-vacuity)", async () => {
    await expect(
      lockChecks.monotonePerKey(makeFakeLockHarness())
    ).resolves.toBeUndefined();
  });

  it("Bus B1 check REJECTS a bus that reconnects without emitting reset", async () => {
    await expect(
      busChecks.resetBeforeFrame(makeForgetfulBusHarness())
    ).rejects.toThrow();
  });
  it("Bus B1 check ACCEPTS the v2 reference (non-vacuity)", async () => {
    await expect(
      busChecks.resetBeforeFrame(makeFakeBusHarness())
    ).resolves.toBeUndefined();
  });

  it("Bus B1′ check REJECTS a bus that establishes without a hello (E21)", async () => {
    await expect(
      busChecks.helloAfterReset(makeSilentBusHarness())
    ).rejects.toThrow();
  });
  it("Bus B1′ check ACCEPTS the reference (non-vacuity — E21)", async () => {
    await expect(
      busChecks.helloAfterReset(makeFakeBusHarness())
    ).resolves.toBeUndefined();
  });

  it("Bus B1′ lower-bound check REJECTS a bus whose hello head under-declares (E21)", async () => {
    await expect(
      busChecks.helloHeadLowerBound(makeUnderdeclaringBusHarness())
    ).rejects.toThrow();
  });
  it("Bus B1′ lower-bound check ACCEPTS the reference (non-vacuity — E21)", async () => {
    await expect(
      busChecks.helloHeadLowerBound(makeFakeBusHarness())
    ).resolves.toBeUndefined();
  });

  it("Bus B2′ lower-bound check REJECTS a bus whose gap head under-declares (E21)", async () => {
    await expect(
      busChecks.gapHeadLowerBound(makeUnderdeclaringGapBusHarness())
    ).rejects.toThrow();
  });
  it("Bus B2′ lower-bound check ACCEPTS the reference (non-vacuity — E21)", async () => {
    await expect(
      busChecks.gapHeadLowerBound(makeFakeBusHarness())
    ).resolves.toBeUndefined();
  });

  it("Bus regId-echo check REJECTS a doBus client that skips the E19 echo assertion (③)", async () => {
    await expect(
      busChecks.regIdEcho(makeNoEchoCheckDoBusHarness())
    ).rejects.toThrow();
  });
  it("Bus regId-echo check ACCEPTS the doBus reference (non-vacuity — ③)", async () => {
    await expect(
      busChecks.regIdEcho(makeDoBusHarness())
    ).resolves.toBeUndefined();
  });

  it("Bus close-on-doubt check REJECTS a doBus server that skips closing a doubtful socket (①)", async () => {
    await expect(
      busChecks.closeOnDoubt(makeNoCloseOnDoubtDoBusHarness())
    ).rejects.toThrow();
  });
  it("Bus close-on-doubt check ACCEPTS the doBus reference (non-vacuity — ①)", async () => {
    await expect(
      busChecks.closeOnDoubt(makeDoBusHarness())
    ).resolves.toBeUndefined();
  });
});
