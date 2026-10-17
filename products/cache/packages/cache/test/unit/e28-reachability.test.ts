// Ordinary unit teeth (NOT sacred). Reachability, split by channel, and the triple.
// Each case bites ONE branch; the named mutation reds it.
//
// THE TRIPLE — the takedown/embargo switch was dead by two mechanisms and misfired by a third:
//   1. SETTABLE   — `takedownSensitive` is reachable through `defaults` (the surface every wrapping
//                   factory threads), not only through the top-level config. Blind either door ⇒ red.
//   2. PRECEDENCE — it OUTRANKS an explicitly-set `unknownPolicy`. Blind it back into an
//                   absent-policy default and an explicit policy shadows it ⇒ red. This is the one
//                   that made the switch unreachable on the entire hero path: the shipped safe
//                   posture sets `unknownPolicy`, so the flag never fired for anyone using it.
//   3. EFFECT     — `'error'` THROWS (`UnknownTagError`) rather than refilling. Blind it back to
//                   `'miss'`'s byte-identical `{validity:'unknown'}` and the read refills ⇒ red.
//
// REACHABILITY:
//   4. strong + registry down ⇒ throws `RegistryUnavailableError` (blind ⇒ a raw error or a serve).
//   5. strong + registry down + `onUnavailable:'eventual'` ⇒ DEGRADES for that call AND emits
//      `strong_degraded` (blind either half ⇒ red — a degrade nobody can see is a misreport).
//   6. CHECK-FAILED ⇒ MISS — an eventual read whose live check FAILS resolves to miss/refill
// (row for an unreachable registry), never stale.
//   7. TWO ORIGINS, TWO RULES — a check-FAILURE bypasses `unknownPolicy`; a SUSPECT-MAP unknown does
//      NOT. Collapsing the two origins into one rule reds, in BOTH directions.
//
// Lane: unit. Deterministic — a hand-built registry whose reachability the case controls, a SimClock,
// and no timing tolerance.

import assert from "node:assert/strict";

import {
  HttpError,
  memory,
  RegistryUnavailableError,
  UnknownTagError,
} from "@astilba/cache";
import type {
  Cache,
  Registry,
  RegistryAck,
  TagKnowledge,
  TelemetryEvent,
} from "@astilba/cache";
import {
  FakeBus,
  FakeRegistry,
  FakeStore,
  mkTag,
  SimClock,
} from "@astilba/harness";
import { describe, expect, it } from "vitest";

import { createCache } from "../../src/cache";

const TAG = mkTag("t0");
/** A SECOND user tag, and the rig teaches the map about exactly one of the pair (see `seedTwoTags`).
 * Without two, `#unknownUserTags` cannot be told apart from a plain reserved-tag filter. */
const TAG_KNOWN = mkTag("t1");

/** A registry whose reachability the test controls. `down` makes every RPC reject the way an
 * unreachable Coordinator does; `checks` counts the live checks that actually happened. */
class SwitchableRegistry implements Registry {
  readonly regId = "e28";
  down = false;
  checks = 0;

  async check(tags: readonly string[]): Promise<readonly TagKnowledge[]> {
    this.checks += 1;
    if (this.down) {
      throw new Error("registry unreachable (simulated)");
    }
    // Everything reads known-clean: the cases below turn on REACHABILITY, never on a watermark.
    return tags.map((): TagKnowledge => ({
      hardEpoch: 0,
      known: true,
      softEpoch: 0,
      throughEpoch: 0,
    }));
  }

  async expire(): Promise<RegistryAck> {
    return { epoch: 0 };
  }

  async delete(): Promise<RegistryAck> {
    return { epoch: 0 };
  }

  async registerRetention(): Promise<void> {
    // No horizon state needed here.
  }
}

interface Rig {
  cache: Cache;
  registry: SwitchableRegistry;
  events: TelemetryEvent[];
}

/** A real cache over a controllable registry. `defaults` is the caller's; nothing else is fixed. */
function rig(
  options: {
    takedownSensitiveTop?: boolean;
    takedownSensitiveDefaults?: boolean;
    unknownPolicy?: "registry-check" | "miss" | "error";
    onUnavailable?: "eventual";
    consistencyDefault?: "strong" | "eventual";
  } = {}
): Rig {
  const clock = new SimClock(0);
  const registry = new SwitchableRegistry();
  const events: TelemetryEvent[] = [];
  const cache = createCache({
    bus: new FakeBus(clock),
    clock,
    l1: memory(),
    l2: memory(),
    namespace: "e28",
    registry,
    rng: { next: () => 0.5 },
    telemetry: (event) => {
      events.push(event);
    },
    defaults: {
      ...(options.consistencyDefault === undefined
        ? {}
        : { consistency: options.consistencyDefault }),
      ...(options.unknownPolicy === undefined
        ? {}
        : { unknownPolicy: options.unknownPolicy }),
      ...(options.onUnavailable === undefined
        ? {}
        : { onUnavailable: options.onUnavailable }),
      ...(options.takedownSensitiveDefaults === undefined
        ? {}
        : { takedownSensitive: options.takedownSensitiveDefaults }),
    },
    ...(options.takedownSensitiveTop === undefined
      ? {}
      : { takedownSensitive: options.takedownSensitiveTop }),
  });
  return { cache, events, registry };
}

/** A retriable origin failure (default classifier) — the grace path's trigger. */
function originDown(): HttpError {
  return new HttpError(new Response(null, { status: 503 }));
}

/** Fill `key` once, so a LATER read has a stored entry to classify. */
async function seed(cache: Cache, key: string): Promise<void> {
  await cache.getOrSet({
    factory: async (): Promise<string> => "v1",
    key,
    tags: [TAG],
  });
}

/**
 * Seed `key` declaring BOTH user tags, with the map taught about `TAG_KNOWN` and NOT about `TAG`.
 * The asymmetry is the whole fixture: it is what makes "the unknown user tags" a DIFFERENT list from
 * "all the user tags", so an assertion can tell them apart.
 *
 * Teaching under `takedownSensitive` takes a STRONG read and nothing else. Strong always runs a live
 * registry check and records what it learns, and it returns from that check without ever
 * reaching the resolution that throws — whereas any eventual read of an unknown tag in this rig
 * throws before it can learn anything. So the education rides a throwaway key that declares only the
 * tag we want known; `TAG` is never checked and stays unknown.
 */
async function seedTwoTags(cache: Cache, key: string): Promise<void> {
  await cache.getOrSet({
    consistency: "strong",
    factory: async (): Promise<string> => "teach",
    key: `${key}/teach`,
    tags: [TAG_KNOWN],
  });
  // The entry under test. A COLD fill classifies nothing, so it cannot throw on the unknown `TAG`.
  await cache.getOrSet({
    factory: async (): Promise<string> => "v1",
    key,
    tags: [TAG, TAG_KNOWN],
  });
}

describe("E9 triple — the takedown switch is settable, outranks, and is distinct in effect", () => {
  it("SETTABLE: `takedownSensitive` arms through `defaults`, the surface a factory threads", async () => {
    // Blind the `defaults` door and this reds: the flag would be reachable only from the raw
    // top-level config, which no wrapping factory forwards — which is how it stayed dead.
    const { cache } = rig({ takedownSensitiveDefaults: true });
    // A COLD fill has no stored entry to classify, so it fills and serves — `unknownPolicy` governs
    // how a STORED entry's unknown tags resolve, and a miss has none. The embargo bites on the read
    // AFTER something is stored, which is exactly the read that could serve purged content.
    await seed(cache, "e9/settable");
    await expect(
      cache.getOrSet({
        factory: async (): Promise<string> => "v2",
        key: "e9/settable",
        tags: [TAG],
      })
    ).rejects.toBeInstanceOf(UnknownTagError);
  });

  it("SETTABLE: it also arms through the top-level config (both doors, same statement)", async () => {
    const { cache } = rig({ takedownSensitiveTop: true });
    await seed(cache, "e9/settable-top");
    await expect(
      cache.getOrSet({
        factory: async (): Promise<string> => "v2",
        key: "e9/settable-top",
        tags: [TAG],
      })
    ).rejects.toBeInstanceOf(UnknownTagError);
  });

  it("PRECEDENCE: it OUTRANKS an explicitly-set unknownPolicy (the hero-path defect)", async () => {
    // `unknownPolicy: 'registry-check'` is EXACTLY what the shipped safe posture stamps. Under the
    // old absent-policy-default reading, the explicit policy shadowed the flag and the read quietly
    // refilled — for every hero-path user, i.e. everyone. Blind the reorder and this reds by
    // resolving 'registry-check' and serving.
    const { cache, registry } = rig({
      takedownSensitiveDefaults: true,
      unknownPolicy: "registry-check",
    });
    await seed(cache, "e9/precedence");
    const checksAfterSeed = registry.checks;
    await expect(
      cache.getOrSet({
        factory: async (): Promise<string> => "v2",
        key: "e9/precedence",
        tags: [TAG],
      })
    ).rejects.toBeInstanceOf(UnknownTagError);
    // …and it never consulted the registry (INV-6): 'error' resolves without a live check. Under the
    // old precedence the explicit 'registry-check' would win, a check would happen, and the value
    // would serve — so this counter is the second half of the same tooth.
    expect(registry.checks).toBe(checksAfterSeed);
  });

  it("EFFECT: 'error' THROWS rather than refilling — distinct from 'miss', which refills", async () => {
    // The two used to be byte-identical (`return {validity:'unknown'}` ⇒ refill), so the switch read
    // as protection and did nothing. THIS is the pair that proves they diverge.
    const embargo = rig({ takedownSensitiveDefaults: true });
    await seed(embargo.cache, "e9/effect");
    let embargoFills = 0;
    await expect(
      embargo.cache.getOrSet({
        factory: async (): Promise<string> => {
          embargoFills += 1;
          return "refilled";
        },
        key: "e9/effect",
        tags: [TAG],
      })
    ).rejects.toBeInstanceOf(UnknownTagError);
    // The read declined to guess: nothing was refetched from an origin that may still be serving
    // content a purge was supposed to remove.
    expect(embargoFills).toBe(0);

    // The CONTROL: the same shape under 'miss' refills happily. Blind the throw back to a plain
    // unknown and the embargo case behaves exactly like this one ⇒ the first expectation reds.
    const plain = rig({ unknownPolicy: "miss" });
    await seed(plain.cache, "e9/effect-control");
    let missFills = 0;
    const served = await plain.cache.getOrSet({
      factory: async (): Promise<string> => {
        missFills += 1;
        return "refilled";
      },
      key: "e9/effect-control",
      tags: [TAG],
    });
    expect(served).toBe("refilled");
    expect(missFills).toBe(1);
  });
});

describe("E28 — reachability (a) the registry RPC (§1)", () => {
  it("a strong read on an unreachable registry throws RegistryUnavailableError (§1's letter)", async () => {
    const { cache, registry } = rig();
    await seed(cache, "reach/strong");
    registry.down = true;
    // the named error, CONSTRUCTED for the first time. Blind it and the caller gets whatever raw
    // thing the transport threw — untyped, uncatchable by contract.
    await expect(
      cache.getOrSet({
        consistency: "strong",
        factory: async (): Promise<string> => "v2",
        key: "reach/strong",
        tags: [TAG],
      })
    ).rejects.toBeInstanceOf(RegistryUnavailableError);
  });

  it("onUnavailable:'eventual' DEGRADES the strong read for that call AND emits strong_degraded", async () => {
    const { cache, registry, events } = rig({ onUnavailable: "eventual" });
    await seed(cache, "reach/degrade");
    registry.down = true;

    // The opt-out's meaning is defined here. The read does NOT throw: it takes
    // the eventual posture — local knowledge, fail-closed on unknown — so it refills rather than
    // failing the request. Blind the degrade and this reds with a RegistryUnavailableError.
    const served = await cache.getOrSet({
      consistency: "strong",
      factory: async (): Promise<string> => "v2",
      key: "reach/degrade",
      tags: [TAG],
    });
    expect(served).toBe("v2");

    // AND IT SAID SO. A degrade the caller opted into is still a degrade an operator must see — a
    // strong read answering with eventual's guarantee, silently, misreports its own health.
    // Blind the emit and the read still "works", which is exactly the failure mode: this assertion
    // is the only thing standing between a silent downgrade and an observable one.
    //
    // ONE EMIT PER DEGRADED RPC, not per read — the raw-fact convention (each event states what
    // happened at one instant, like `store_read_suppressed`). This read degrades at three sites and
    // emits three times (measured): L1's classify, L2's classify, and the fill's own pre-fetch mint.
    // Asserted as "at least one, each carrying its reason" rather than as the literal 3, because the
    // count is a property of how many tiers this rig composes, while the CONTRACT is that a degrade
    // is never silent.
    const degraded = events.filter((e) => e.type === "strong_degraded");
    expect(degraded.length).toBeGreaterThan(0);
    // A reason CLASS only — never the registry's own error text (a payload safe for a plain sink).
    for (const event of degraded) {
      expect(event.reason).toBe("registry_unreachable");
    }
  });

  it("the degrade emits from the CLASSIFY site too — a degraded read that serves locally is not silent", async () => {
    // WHY THIS CASE EXISTS, stated plainly: there are TWO registry-RPC sites on the strong path —
    // `validate`'s live check and the fill's pre-fetch mint — and the case above cannot tell them
    // apart, because a degraded read that REFILLS passes through both. Blinding either one alone
    // leaves it green (measured, not assumed). So this case drives a degrade that serves WITHOUT
    // refilling, which reaches the classify site and nothing else.
    const { cache, registry, events } = rig({ onUnavailable: "eventual" });
    await seed(cache, "reach/local");
    // A healthy eventual read teaches the map: the tag becomes locally KNOWN-clean (memoization).
    await cache.getOrSet({
      factory: async (): Promise<string> => "unused",
      key: "reach/local",
      tags: [TAG],
    });

    registry.down = true;
    let refills = 0;
    const served = await cache.getOrSet({
      consistency: "strong",
      factory: async (): Promise<string> => {
        refills += 1;
        return "v2";
      },
      key: "reach/local",
      tags: [TAG],
    });
    // The degrade IS the eventual posture: local knowledge says the tag is clean, so the stored value
    // serves and no fill runs — which is why the fill's emit site is never reached here.
    expect(served).toBe("v1");
    expect(refills).toBe(0);
    // Blind the classify-site emit and THIS reds, where the refilling case above does not.
    expect(
      events.filter((e) => e.type === "strong_degraded").length
    ).toBeGreaterThan(0);
  });

  it("a HEALTHY strong read neither throws nor emits strong_degraded (the other direction)", async () => {
    // Non-vacuity for the pair above: the degrade fires on the failure, not on every strong read.
    const { cache, events } = rig({ onUnavailable: "eventual" });
    await seed(cache, "reach/healthy");
    const served = await cache.getOrSet({
      consistency: "strong",
      factory: async (): Promise<string> => "v2",
      key: "reach/healthy",
      tags: [TAG],
    });
    // The registry is up and says the tag is clean, so the STORED value serves — no refill.
    expect(served).toBe("v1");
    expect(events.filter((e) => e.type === "strong_degraded")).toHaveLength(0);
  });
});

describe("E28 — check-failed ⇒ miss, and the two-origins rule (§5.1/5a, §9, E20)", () => {
  it("CHECK-FAILED ⇒ MISS: an eventual read whose live check fails refills (never stale)", async () => {
    // the answer when the registry cannot be reached is to assume the worst about the entry and
    // go back to origin, which is safe exactly because origin is the truth. The failure matrix says the same from
    // the other side: an unknown resolves to a miss and never to a stale serve. Blind this to serve
    // the stored value instead and it reds — the blinded version hands back possibly-purged content
    // for no better reason than that the authority on the question is down.
    const { cache, registry } = rig({ unknownPolicy: "registry-check" });
    await seed(cache, "origins/check-failed");
    registry.down = true;

    let refills = 0;
    const served = await cache.getOrSet({
      factory: async (): Promise<string> => {
        refills += 1;
        return "v2";
      },
      key: "origins/check-failed",
      tags: [TAG],
    });
    expect(served).toBe("v2");
    expect(refills).toBe(1);
  });

  it("TWO ORIGINS: a check-FAILURE bypasses unknownPolicy — it is not re-routed through the check that failed", async () => {
    // The collapse this forbids: routing a check-failure back through `unknownPolicy`, when that
    // policy's own default is to run the check that has just failed — so consulting it would either
    // loop or answer with the broken mechanism. Under `takedownSensitive` the resolution is 'error' and
    // NEVER reaches a check at all (proven above); under 'registry-check' the failure resolves by
    // unknown resolution directly. So a failed check must NOT consult the policy — and the observable consequence
    // is that the read REFILLS instead of throwing or looping.
    const { cache, registry } = rig({ unknownPolicy: "registry-check" });
    await seed(cache, "origins/bypass");
    registry.down = true;
    const before = registry.checks;

    const served = await cache.getOrSet({
      factory: async (): Promise<string> => "v2",
      key: "origins/bypass",
      tags: [TAG],
    });
    expect(served).toBe("v2");
    // AT MOST ONE CHECK PER TIER CONSULTED (L1, then L2 — each classifies its own read, the read path
    // 2/4), and never a SECOND for the same unknown. The measured number is 2, and the number is the
    // tooth: re-route the catch through `unknownResolution` and a 'registry-check' policy answers
    // "check again" to the failure of the check it just made — so each tier checks twice and this
    // reds at 4. (It cannot terminate any other way: the policy's own default IS the mechanism that
    // is down, which is precisely why unknown resolution rules this origin directly instead.)
    expect(registry.checks - before).toBe(2);
  });

  it("TWO ORIGINS (the other rule): a SUSPECT-MAP unknown DOES resolve per unknownPolicy (E20 (i))", async () => {
    // The counterpart, and the direction a collapse breaks the other way: unknowns whose origin is
    // the MAP (not a failed check) go through the policy exactly as unknown resolution rules. Here the registry is
    // UP, the map knows nothing, and the policy is 'registry-check' — so a check happens and the
    // stored value is confirmed and served. Collapse the origins so every unknown is treated as a
    // check-failure ⇒ no check is made, the read refills ⇒ both assertions red.
    const { cache, registry } = rig({ unknownPolicy: "registry-check" });
    await seed(cache, "origins/suspect");
    const before = registry.checks;

    let refills = 0;
    const served = await cache.getOrSet({
      factory: async (): Promise<string> => {
        refills += 1;
        return "v2";
      },
      key: "origins/suspect",
      tags: [TAG],
    });
    // The policy was consulted and it consulted the registry, which vouched for the tag.
    expect(registry.checks).toBeGreaterThan(before);
    expect(served).toBe("v1");
    expect(refills).toBe(0);
  });

  it("TWO ORIGINS: a map-origin unknown under 'miss' refills WITHOUT a check (E20 (i), INV-6)", async () => {
    // The same origin, a different policy — proving the policy is genuinely in charge of this origin
    // rather than incidentally agreeing with unknown resolution. 'miss' never consults the registry (INV-6).
    const { cache, registry } = rig({ unknownPolicy: "miss" });
    await seed(cache, "origins/miss");
    const before = registry.checks;
    const served = await cache.getOrSet({
      factory: async (): Promise<string> => "v2",
      key: "origins/miss",
      tags: [TAG],
    });
    expect(served).toBe("v2");
    expect(registry.checks).toBe(before);
  });
});

// ── THE SERVE-TIME RE-VALIDATION OF A GRACED CANDIDATE ────────────────────────
// The strong-path registry-RPC site that sits INSIDE a failed fill: the factory has already errored,
// and this check decides whether the stale copy may still be served. At `strong` it is a live
// registry check exactly like the other three, so it honors `onUnavailable` exactly like them.
//
// A SEPARATE RIG, AND HERE IS WHY. The rig above answers every check known-clean, so nothing there
// can ever be STALE — and without a stale copy there is no grace candidate and this site is
// unreachable. Staleness has only one author (a real soft purge that reaches the isolate over the
// bus), so these cases compose the harness's real registry + bus and wrap the registry in a switch.
//
// THE TRACE IS THE REAL ONE, not a contrivance: the registry answers when the read classifies and is
// gone by the time the candidate is re-validated — which is precisely what a registry going down
// during a slow origin call looks like. The factory takes it down on its way out.
describe("E28 — the graced candidate's serve-time re-validation honors onUnavailable", () => {
  /** The harness's real registry, wrapped in a reachability switch. Purges and watermarks are the
   * real fake's (staleness needs a real author); only reachability is the test's. */
  class SwitchableFakeRegistry implements Registry {
    readonly regId: string;
    down = false;

    constructor(private readonly inner: FakeRegistry) {
      this.regId = inner.regId;
    }

    async check(tags: readonly string[]): Promise<readonly TagKnowledge[]> {
      if (this.down) {
        throw new Error("registry unreachable (simulated)");
      }
      return await this.inner.check(tags);
    }

    async expire(tags: readonly string[]): Promise<RegistryAck> {
      return await this.inner.expire(tags);
    }

    async delete(tags: readonly string[]): Promise<RegistryAck> {
      return await this.inner.delete(tags);
    }

    async registerRetention(retentionMs: number): Promise<void> {
      await this.inner.registerRetention(retentionMs);
    }
  }

  interface GraceRig {
    cache: Cache;
    registry: SwitchableFakeRegistry;
    events: TelemetryEvent[];
    advance: (ms: number) => Promise<void>;
  }

  function graceRig(onUnavailable?: "eventual"): GraceRig {
    const clock = new SimClock(0);
    const bus = new FakeBus(clock);
    const store = new FakeStore(clock, { replicas: 1 });
    const inner = new FakeRegistry(clock, {
      bus,
      regId: "grace",
      store: store.replica(0),
    });
    const registry = new SwitchableFakeRegistry(inner);
    const events: TelemetryEvent[] = [];
    const cache = createCache({
      bus,
      clock,
      l1: memory(),
      l2: store.replica(0),
      namespace: "grace",
      registry,
      rng: { next: () => 0.5 },
      telemetry: (event) => {
        events.push(event);
      },
      ...(onUnavailable === undefined ? {} : { defaults: { onUnavailable } }),
    });
    return {
      advance: async (ms: number): Promise<void> => {
        clock.advance(ms);
        bus.deliverDue(clock.now());
        inner.pumpChecks(clock.now());
        await clock.runMicrotasks();
      },
      cache,
      events,
      registry,
    };
  }

  /** Fill once, then soft-purge so the copy is STALE. At `strong` a stale copy routes to the
   * FOREGROUND fill (strong forgoes SWR), so a failing factory meets it as the grace
   * CANDIDATE, which is the branch under test. */
  async function seedThenSoftPurge(
    rigged: GraceRig,
    key: string
  ): Promise<void> {
    await rigged.cache.getOrSet({
      factory: async (): Promise<string> => "v1",
      grace: "1h",
      key,
      tags: [TAG],
    });
    await rigged.cache.expire({ tag: TAG });
    await rigged.advance(1);
  }

  it("DEFAULT: registry unreachable at serve-time re-validation ⇒ RegistryUnavailableError carrying the factory error as `cause`", async () => {
    const rigged = graceRig();
    await seedThenSoftPurge(rigged, "grace/throw");

    const factoryError = originDown();
    const thrown = await rigged.cache
      .getOrSet({
        consistency: "strong",
        factory: async (): Promise<string> => {
          // Reachable at classify, gone by re-validation — the registry dies during the fill.
          rigged.registry.down = true;
          throw factoryError;
        },
        grace: "1h",
        key: "grace/throw",
        tags: [TAG],
      })
      .then(
        () => {},
        (error: unknown) => error
      );

    // ARM 1. The site is a strong live check, so an unreachable registry means this read cannot make
    // the promise `strong` is defined by, and it says so. Leave the site unguarded and the throw
    // still happens — but from inside `validate`, at a caller who explicitly opted out.
    // `assert.ok` narrows without an assertion cast, and fails the case if the type is wrong.
    assert.ok(thrown instanceof RegistryUnavailableError);
    // THE CAUSE CHAIN, which is what makes guarding this site cost nothing. This error displaced the
    // factory's, so the factory's rides along. Blind the `cause` and the origin failure that started
    // the fill vanishes from the caller's hands ⇒ red.
    expect(thrown.cause).toBe(factoryError);
  });

  it("'eventual': the same site DEGRADES instead of throwing, and the candidate resolves on local knowledge", async () => {
    const rigged = graceRig("eventual");
    await seedThenSoftPurge(rigged, "grace/degrade");

    const served = await rigged.cache.getOrSet({
      consistency: "strong",
      factory: async (): Promise<string> => {
        rigged.registry.down = true;
        throw originDown();
      },
      grace: "1h",
      key: "grace/degrade",
      tags: [TAG],
    });

    // ARM 2. The caller opted out, so the site does NOT throw: it degrades for this call, and the
    // candidate resolves per the grace law on local knowledge. That knowledge is COMPLETE here — the
    // bus delivered the soft purge — so the tag's hardness IS established: the copy is stale, not
    // unknown, and grace serves it. Origin down, registry down, and the reader still answers
    // correctly, which is the entire point of the opt-out.
    //
    // Stop forwarding `onUnavailable` here (the defect) and this reds with a RegistryUnavailableError
    // thrown at the one caller who explicitly asked never to see one.
    expect(served).toBe("v1");

    // …and the degrade is announced rather than silent.
    const degraded = rigged.events.filter((e) => e.type === "strong_degraded");
    expect(degraded.length).toBeGreaterThan(0);
    for (const event of degraded) {
      expect(event.reason).toBe("registry_unreachable");
    }
  });
});

// ── THE FILL SITE'S OWN GUARD, REACHED ──────────────────────────────────────────
// The strong fill mints its bornEpoch from a live check BEFORE running the factory. That check has
// its own `onUnavailable` guard, and until now nothing reached it: every strong-throw case above SEEDS
// the key first, so the read's classify site throws while the fill site sits untouched behind it.
// Blinding the fill's guard therefore left the whole suite green.
//
// A COLD key is what separates them. With nothing stored there is no envelope to classify, so the
// read goes straight to the fill and the ONLY live check on the call is the fill's own.
describe("E28 — the strong fill's pre-factory live check honors onUnavailable (reached cold)", () => {
  it("DEFAULT: a COLD strong fill against an unreachable registry throws RegistryUnavailableError from the fill site", async () => {
    const { cache, registry } = rig();
    registry.down = true;
    let factoryRuns = 0;

    // Nothing seeded: this key has never existed, so no classify site is on the path.
    await expect(
      cache.getOrSet({
        consistency: "strong",
        factory: async (): Promise<string> => {
          factoryRuns += 1;
          return "v1";
        },
        key: "fill/cold-throw",
        tags: [TAG],
      })
    ).rejects.toBeInstanceOf(RegistryUnavailableError);

    // AND IT REFUSED BEFORE THE FACTORY. The check mints the bornEpoch the fill is anchored on, so a
    // fill that cannot mint must not run: a value born at an unverified epoch is the overstatement of
    // recency the consistency model forbids. Blind the guard (`onUnavailable !== "eventual"` ⇒ always false) and this
    // reds twice over — the raw registry error escapes untyped AND the factory runs anyway.
    expect(factoryRuns).toBe(0);
  });

  it("'eventual': the same COLD fill degrades, mints from local knowledge, runs the factory, and emits strong_degraded", async () => {
    const { cache, registry, events } = rig({ onUnavailable: "eventual" });
    registry.down = true;
    let factoryRuns = 0;

    const served = await cache.getOrSet({
      consistency: "strong",
      factory: async (): Promise<string> => {
        factoryRuns += 1;
        return "v1";
      },
      key: "fill/cold-degrade",
      tags: [TAG],
    });

    // THE OTHER DIRECTION. The caller opted out, so the fill does not throw: it mints from what this
    // isolate has actually verified — an understatement of recency, which over-invalidates and costs
    // a refill, never a serve against an unseen purge. Blind the degrade and this reds with a throw.
    expect(served).toBe("v1");
    expect(factoryRuns).toBe(1);

    // And the fill's degrade is announced from the fill site, not merely from the read's classify —
    // this call has no classify site at all, so every event here is the fill's own.
    const degraded = events.filter((e) => e.type === "strong_degraded");
    expect(degraded.length).toBeGreaterThan(0);
    for (const event of degraded) {
      expect(event.reason).toBe("registry_unreachable");
    }
  });
});

// ── THE SURFACES THAT DESCRIBED THEMSELVES WRONGLY ──────────────────────────────────────
// Two declared-but-unhonored surfaces. Neither is exotic: each is a field whose documentation states
// a contract the code did not keep. That shape is the hardest to catch by reading, because the doc
// is what a reader checks the code against, and here the doc is the thing that is wrong.
describe("E9/E28 — UnknownTagError says exactly what it promises", () => {
  it("carries EXACTLY the unknown USER tags — not the entry's whole declared set, and never a reserved tag", async () => {
    const { cache } = rig({ takedownSensitiveDefaults: true });
    // TWO declared user tags, exactly ONE of them unknown. That is what gives this case its subject.
    await seedTwoTags(cache, "payload/tags");

    const thrown = await cache
      .getOrSet({
        factory: async (): Promise<string> => "v2",
        key: "payload/tags",
        tags: [TAG, TAG_KNOWN],
      })
      .then(
        () => {},
        (error: unknown) => error
      );

    assert.ok(thrown instanceof UnknownTagError);
    const { tags } = thrown;

    // FIXTURE REACH: A RIG WHERE THE UNKNOWN TAGS AND ALL THE TAGS ARE THE SAME LIST CERTIFIES
    // NOTHING ABOUT THE FILTER. This case used to declare ONE user tag, which made
    // `#unknownUserTags(tags)` and the entry's whole declared user set the identical array — so
    // `toStrictEqual([TAG])` passed even with the unknown-ness test deleted and only the
    // reserved-tag filter left, and the `unknown` half of the branch had no coverage at all.
    // Measured, not reasoned: the old rig runs GREEN against the mutation below. A tooth can only
    // bite a distinction its fixture is able to express.
    //
    // THE TOOTH, now that it has one. The stored set is `[t0, t1, __k:<hash>, __ns:e28]`; `t1` is
    // known-clean and `t0` is not. The payload must be `t0` ALONE:
    //   • drop the unknown-ness filter (report every declared user tag) ⇒ `[t0, t1]` ⇒ RED. This is
    //     the arm the one-tag rig could never see.
    //   • drop the reserved filter (pass the raw set) ⇒ the `__` tags ride along ⇒ RED. An
    //     app-facing error has no business handing out the kernel's bookkeeping or a key hash.
    // What the caller is being asked is WHICH dependency is embargoed. A list of everything the
    // entry happens to declare is not an answer to that question.
    expect(tags).toStrictEqual([TAG]);
    expect(tags.some((tag) => tag.startsWith("__"))).toBeFalsy();
  });

  it("names the switch that ACTUALLY armed it — takedownSensitive, not the policy it outranks", async () => {
    const { cache } = rig({ takedownSensitiveDefaults: true });
    await seed(cache, "payload/message");

    const thrown = await cache
      .getOrSet({
        factory: async (): Promise<string> => "v2",
        key: "payload/message",
        tags: [TAG],
      })
      .then(
        () => {},
        (error: unknown) => error
      );

    // THE TOOTH. `takedownSensitive` armed this read — it OUTRANKS `unknownPolicy`, so the policy
    // named in the old message was, on this exact path, the one that did NOT fire. An operator
    // reading it would go looking at the wrong switch. Hard-code the cause again and this reds.
    assert.ok(thrown instanceof Error);
    expect(thrown.message).toContain("takedownSensitive is set");
    expect(thrown.message).not.toContain("unknownPolicy is 'error'");
  });

  it("…and still names unknownPolicy when THAT is what armed it (the other direction)", async () => {
    const { cache } = rig({ unknownPolicy: "error" });
    await seed(cache, "payload/policy");

    const thrown = await cache
      .getOrSet({
        factory: async (): Promise<string> => "v2",
        key: "payload/policy",
        tags: [TAG],
      })
      .then(
        () => {},
        (error: unknown) => error
      );

    // Non-vacuity for the case above: the message tracks the armer rather than being rewritten to a
    // new constant. Collapse the two arms either way and one of these two cases reds.
    assert.ok(thrown instanceof Error);
    expect(thrown.message).toContain("unknownPolicy is 'error'");
  });
});

describe("E28 — CacheDefaults.consistency is a setting, not a decoration", () => {
  /** Fill `key`, then read it once at EVENTUAL so the live check teaches the map the tag is
   * known-clean (memoization). After this, an eventual read consults NOBODY and a strong read
   * still live-checks every time (strong never consults local knowledge). That asymmetry is
   * the only thing that can tell the two postures apart by counting: before the map is taught, an
   * eventual read of an unknown tag pays exactly one check too, and the counter says nothing. */
  async function seedAndTeach(rigged: Rig, key: string): Promise<void> {
    await rigged.cache.getOrSet({
      consistency: "eventual",
      factory: async (): Promise<string> => "v1",
      key,
      tags: [TAG],
    });
    await rigged.cache.getOrSet({
      consistency: "eventual",
      factory: async (): Promise<string> => "unused",
      key,
      tags: [TAG],
    });
  }

  it("a defaulted `strong` takes the strong path (a live registry check), with no per-call option", async () => {
    const rigged = rig({ consistencyDefault: "strong" });
    await seedAndTeach(rigged, "defaults/consistency");
    const before = rigged.registry.checks;

    // No `consistency` on the call: the INSTANCE declared it.
    await rigged.cache.getOrSet({
      factory: async (): Promise<string> => "v2",
      key: "defaults/consistency",
      tags: [TAG],
    });

    // THE TOOTH. Strong live-checks even against a map that already knows the tag. Ignore
    // `config.defaults.consistency` (the defect: `options.consistency ?? "eventual"`) and this read
    // runs eventual, resolves from the taught map, consults nobody — the counter does not move and
    // this reds. The declared field did nothing, silently, for anyone who set it.
    expect(rigged.registry.checks).toBeGreaterThan(before);
  });

  it("a per-call consistency still OUTRANKS the instance default (precedence, not replacement)", async () => {
    const rigged = rig({ consistencyDefault: "strong" });
    await seedAndTeach(rigged, "defaults/override");
    const before = rigged.registry.checks;

    // The call says eventual; the instance says strong. Per-call wins — the default is a default.
    await rigged.cache.getOrSet({
      consistency: "eventual",
      factory: async (): Promise<string> => "v2",
      key: "defaults/override",
      tags: [TAG],
    });

    // ZERO further checks: the taught map answers, so the strong path was not taken. Wire the
    // default as an OVERRIDE (`config.defaults?.consistency ?? options.consistency`) and strong's
    // live check fires ⇒ red.
    expect(rigged.registry.checks).toBe(before);
  });
});
