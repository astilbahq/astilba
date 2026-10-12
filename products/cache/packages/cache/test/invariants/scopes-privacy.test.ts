// INV-4  — spec §6/§11.4, errata E17
// INV-15 — spec §11.15 (MI4)
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.

import { createCache } from "@astilba/cache";
import type { TelemetryEvent } from "@astilba/cache";
import {
  FakeStore,
  guardedRequest,
  hostedTelemetry,
  mkTag,
  SimClock,
  SimRng,
  world,
} from "@astilba/harness";
import { test } from "@fast-check/vitest";
import { describe, expect } from "vitest";

// The `singleflight_option_mismatch` event (§10) carries the colliding key ref and the
// declared tag refs as distinct fields. Pull EACH out on its own so per-field salting can
// be asserted — a whole-event diff cannot tell "both refs salted" from "only one salted".
const mismatchEvent = (
  events: readonly TelemetryEvent[]
): TelemetryEvent | undefined =>
  events.find((event) => event.type === "singleflight_option_mismatch");
const keyRefOf = (events: readonly TelemetryEvent[]): unknown =>
  mismatchEvent(events)?.key;
const tagRefOf = (events: readonly TelemetryEvent[]): unknown =>
  mismatchEvent(events)?.tags;

describe("scopes & privacy", () => {
  test("INV-4: a private (usr:) entry never reaches a shared tier", async () => {
    const w = world({ isolates: 2 });
    const a = w.isolate(0).cache;
    const b = w.isolate(1).cache;
    const tag = mkTag("t0");
    const first = await a.getOrSetEntry({
      factory: async () => await Promise.resolve("private-value"),
      key: "profile",
      request: { userId: "u1" },
      tags: [tag],
    });
    expect(first.value).toBe("private-value");
    expect(first.durable).toBeFalsy(); // private ⇒ L1-only, not durable to a shared tier
    // A different isolate cannot read the private value from any shared tier.
    const second = await b.getOrSetEntry({
      factory: async () => await Promise.resolve("refilled"),
      key: "profile",
      tags: [tag],
    });
    expect(second.value).toBe("refilled");
  });

  test("INV-4: a public-scoped factory touching a guarded accessor never yields a shared-tier entry", async () => {
    const w = world({ dev: true, isolates: 2, replicas: 2 });
    const producer = w.isolate(0).cache;
    const reader = w.isolate(1).cache;
    const tag = mkTag("t0");
    // CONTROL A — public, no request at all: durable + shared.
    const noRequest = await producer.getOrSetEntry({
      factory: async () => await Promise.resolve("a"),
      key: "control-a",
      scope: "public",
      tags: [tag],
    });
    expect(noRequest.durable).toBeTruthy();
    // CONTROL B (the discriminator) — public, request PRESENT but the factory never
    // touches it: still durable. A kernel that demoted merely because a request was
    // ATTACHED (not READ) would wrongly demote this one; a demote-on-read kernel does not.
    const unread = guardedRequest({ tenant: "acme", userId: "u1" });
    const presentUnread = await producer.getOrSetEntry({
      // arity-1 like THE CASE, and it DOES touch the ctx — but only `ctx.signal`, a
      // sanctioned accessor, never `ctx.request`. So the sole difference from THE CASE
      // is the guarded property read (not factory arity or ctx presence).
      factory: async (ctx) =>
        await Promise.resolve(ctx.signal.aborted ? "aborted-b" : "b"),
      key: "control-b",
      request: unread.request,
      scope: "public",
      tags: [tag],
    });
    expect(presentUnread.durable).toBeTruthy();
    // THE CASE — a public factory that READS a guarded request accessor. Per §6 the
    // kernel demotes the resulting entry off the shared tier. The ONLY difference from
    // CONTROL B is the property read, so demotion here is caused by the guarded touch.
    const guard = guardedRequest({ tenant: "acme", userId: "u1" });
    const demoted = await producer.getOrSetEntry({
      factory: async (ctx) =>
        await Promise.resolve(`page-for-${String(ctx.request?.userId)}`),
      key: "guarded",
      request: guard.request,
      scope: "public",
      tags: [tag],
    });
    expect(guard.reads).toContain("userId");
    expect(demoted.durable).toBeFalsy();
    // Invisible to another isolate — not published to the shared tier.
    const other = await reader.getOrSetEntry({
      factory: async () => await Promise.resolve("refilled"),
      key: "guarded",
      tags: [tag],
    });
    expect(other.value).toBe("refilled");
  });

  test("INV-15: hosted telemetry carries no raw keys/tags; key AND tag hashes are per-project salted", async () => {
    const rawKey = "secret-user-key-9f3a";
    const otherKey = "secret-user-key-1c7d";
    const rawTag = "secret-tag-b71c";
    const otherTag = "secret-tag-e05a";
    const mismatchTag = "mismatch-tag";
    const saltA = "project-A-salt";
    const saltB = "project-B-salt";
    // A concurrent same-key fill pair with INCOMPATIBLE options (the first carries `tag`,
    // the second a fixed distinct tag) reliably emits `singleflight_option_mismatch` in
    // prod mode — an event ABOUT the colliding key and mismatched tags (§2.1, §10), so a
    // captured event definitely references both, anchoring the redaction checks below.
    const drive = async (
      key: string,
      tag: string,
      salt: string
    ): Promise<readonly TelemetryEvent[]> => {
      const clock = new SimClock();
      const store = new FakeStore(clock, { replicas: 1 });
      const hosted = hostedTelemetry(salt);
      const cache = createCache({
        clock,
        dev: false,
        l2: store.replica(0),
        namespace: "journals",
        rng: new SimRng(1),
        telemetry: hosted.config,
      });
      await Promise.all([
        cache.getOrSet({
          factory: async () => await Promise.resolve("a"),
          key,
          tags: [mkTag(tag)],
        }),
        cache.getOrSet({
          factory: async () => await Promise.resolve("b"),
          key,
          tags: [mkTag(mismatchTag)],
        }),
      ]);
      return hosted.events;
    };
    // Vary salt, key, and tag independently around a base drive.
    const base = await drive(rawKey, rawTag, saltA);
    const altSalt = await drive(rawKey, rawTag, saltB);
    const altKey = await drive(otherKey, rawTag, saltA);
    const altTag = await drive(rawKey, otherTag, saltA);
    // Non-vacuous: each drive emitted the mismatch event, and it names both a key ref and
    // a tag ref (so the per-field comparisons below actually compare something).
    for (const events of [base, altSalt, altKey, altTag]) {
      expect(mismatchEvent(events)).toBeDefined();
      expect(keyRefOf(events)).toBeDefined();
      expect(tagRefOf(events)).toBeDefined();
    }
    // Hosted output never carries the raw key, raw tag, or the per-project salt (a secret).
    for (const { events, key, tag } of [
      { events: base, key: rawKey, tag: rawTag },
      { events: altSalt, key: rawKey, tag: rawTag },
      { events: altKey, key: otherKey, tag: rawTag },
      { events: altTag, key: rawKey, tag: otherTag },
    ]) {
      for (const event of events) {
        const serialized = JSON.stringify(event);
        expect(serialized).not.toContain(key);
        expect(serialized).not.toContain(tag);
        expect(serialized).not.toContain(saltA);
        expect(serialized).not.toContain(saltB);
      }
    }
    // Per-field SALT coverage — the crux. Hold key AND tag fixed and change ONLY the
    // project salt: BOTH the key ref and the tag ref must change. A kernel that salts the
    // key hash but forwards an unsalted tag hash (or vice versa) survives a whole-event
    // diff but is caught by exactly one of these two lines.
    expect(keyRefOf(base)).not.toStrictEqual(keyRefOf(altSalt));
    expect(tagRefOf(base)).not.toStrictEqual(tagRefOf(altSalt));
    // Per-field keying — the key ref tracks the key input; the tag ref tracks the tag
    // input. With no raw key/tag/salt present, together with the salt axis this pins each
    // ref as a per-project SALTED hash of its own identifier (MI4), not an incidental diff.
    expect(keyRefOf(base)).not.toStrictEqual(keyRefOf(altKey));
    expect(tagRefOf(base)).not.toStrictEqual(tagRefOf(altTag));
  });
});
