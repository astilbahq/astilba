import type {
  Store,
  StoreValue,
  StoreWriteError,
  StoreWriteOptions,
  TelemetryEvent,
} from "@astilba/cache";
import { createCache } from "@astilba/cache";
import { SimClock, SimRng } from "@astilba/harness";
import { describe, expect, test } from "vitest";

// Unit coverage for the L2 write-back failure classification wired in.
// The sacred suite CANNOT reach this branch — its only injected store failure carries no
// `code`, so it lands on the unclassified (propagating) path. Without these tests the
// suppression path, its `durable:false` surfacing, and the two reserved telemetry names
// would ship unexercised: a check that cannot reject a liar proves nothing.
//
// The classification is structural (`isStoreWriteError`), never `instanceof`, so these
// fixtures deliberately share NO class with the kernel or the harness — that is the whole
// point of the contract.

/** A `Store` whose `set` always rejects with the given classified failure. */
class ClassifiedFailStore implements Store {
  readonly #data = new Map<string, string>();
  readonly #failure: () => Error;

  constructor(failure: () => Error) {
    this.#failure = failure;
  }

  async get(key: string): Promise<StoreValue | undefined> {
    const value = this.#data.get(key);
    return value === undefined ? undefined : { value };
  }

  async set(
    _key: string,
    _value: string,
    _options?: StoreWriteOptions
  ): Promise<void> {
    throw this.#failure();
  }

  async delete(key: string): Promise<void> {
    this.#data.delete(key);
  }
}

/** A `Store` that accepts every write — the control arm (non-vacuity). */
class AcceptingStore implements Store {
  readonly #data = new Map<string, string>();

  async get(key: string): Promise<StoreValue | undefined> {
    const value = this.#data.get(key);
    return value === undefined ? undefined : { value };
  }

  async set(key: string, value: string): Promise<void> {
    this.#data.set(key, value);
  }

  async delete(key: string): Promise<void> {
    this.#data.delete(key);
  }
}

// Classified failures. Error subclasses (oxlint forbids throwing a plain object) that
// SATISFY the structural contract without sharing a class with the kernel or harness.
class ThrottledWriteError extends Error implements StoreWriteError {
  readonly code = "throttled";
  readonly retryable = true;

  constructor(message: string) {
    super(message);
    this.name = "ThrottledWriteError";
  }
}
class UnavailableWriteError extends Error implements StoreWriteError {
  readonly code = "unavailable";
  readonly retryable = true;

  constructor(message: string) {
    super(message);
    this.name = "UnavailableWriteError";
  }
}
class TooLargeWriteError extends Error implements StoreWriteError {
  readonly code = "too_large";
  readonly retryable = false;

  constructor(message: string) {
    super(message);
    this.name = "TooLargeWriteError";
  }
}

function harness(l2: Store): {
  cache: ReturnType<typeof createCache>;
  events: TelemetryEvent[];
} {
  const events: TelemetryEvent[] = [];
  const cache = createCache({
    clock: new SimClock(),
    l2,
    namespace: "wb",
    rng: new SimRng(1),
    telemetry: (event: TelemetryEvent): void => {
      events.push(event);
    },
  });
  return { cache, events };
}

const fill = async (
  cache: ReturnType<typeof createCache>
): Promise<{ readonly durable?: boolean; readonly value: unknown }> =>
  await cache.getOrSetEntry({
    factory: async () => await Promise.resolve("v"),
    key: "k",
  });

const types = (events: readonly TelemetryEvent[]): readonly string[] =>
  events.map((e) => e.type);

describe("L2 write-back failure classification (§5.0, E13)", () => {
  test("control: a write-back that lands is durable and emits no failure telemetry", async () => {
    const { cache, events } = harness(new AcceptingStore());
    const entry = await fill(cache);
    expect(entry.value).toBe("v");
    // Non-vacuity: the suppression assertions below mean nothing unless the happy path
    // genuinely reports `durable:true` with no failure event.
    expect(entry.durable).toBeTruthy();
    expect(types(events)).not.toContain("writeback_throttled");
    expect(types(events)).not.toContain("l2_write_error");
  });

  test("`throttled` is suppressed: the value still serves, durable:false, writeback_throttled", async () => {
    const { cache, events } = harness(
      new ClassifiedFailStore(() => new ThrottledWriteError("throttled"))
    );
    const entry = await fill(cache);
    // Suppressed, not failed: the caller still gets the value.
    expect(entry.value).toBe("v");
    expect(entry.durable).toBeFalsy();
    expect(types(events)).toContain("writeback_throttled");
  });

  test("`unavailable` is suppressed: durable:false, l2_write_error", async () => {
    const { cache, events } = harness(
      new ClassifiedFailStore(() => new UnavailableWriteError("down"))
    );
    const entry = await fill(cache);
    expect(entry.value).toBe("v");
    expect(entry.durable).toBeFalsy();
    expect(types(events)).toContain("l2_write_error");
  });

  test("`too_large` is permanent: it PROPAGATES and is never silently suppressed", async () => {
    const { cache, events } = harness(
      new ClassifiedFailStore(() => new TooLargeWriteError("25 MiB"))
    );
    // A value that can never be stored is a caller bug, not a transient — suppressing it
    // would hide the defect and report a durability the store can never provide.
    await expect(fill(cache)).rejects.toThrow(TooLargeWriteError);
    expect(types(events)).not.toContain("writeback_throttled");
    // Discriminating: a CLASSIFIED `too_large` propagates on its own and emits nothing.
    // Only an unclassified rejection is surfaced as `l2_write_error` — so this assertion
    // fails if the structural classification stops recognizing the shape.
    expect(types(events)).not.toContain("l2_write_error");
  });

  test("an UNCLASSIFIED rejection keeps today's behavior: it propagates, with l2_write_error observability", async () => {
    const { cache, events } = harness(
      new ClassifiedFailStore(() => new Error("boom"))
    );
    // The classification wiring must never start swallowing errors that used to surface.
    await expect(fill(cache)).rejects.toThrow("boom");
    expect(types(events)).toContain("l2_write_error");
  });
});
