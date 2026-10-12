// INV-7  — spec §11.7
// INV-26 — errata E7 (§11 invariant amendment)
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.

import { HttpError } from "@astilba/cache";
import { commandsArb, mkTag, modelSetup, world } from "@astilba/harness";
import { test } from "@fast-check/vitest";
import fc from "fast-check";
import { describe, expect } from "vitest";

const notFound = async (): Promise<never> =>
  await Promise.reject(new HttpError(new Response(null, { status: 404 })));
const serverError = async (): Promise<never> =>
  await Promise.reject(new HttpError(new Response(null, { status: 503 })));

describe("negative caching", () => {
  test("INV-7: a negative (404) entry is never grace- or stale-if-error-served", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    const first = await cache.getOrSetEntry({
      factory: notFound,
      grace: "1h",
      key: "k",
      notFoundTtl: "30s",
      tags: [tag],
    });
    expect(first.value).toBeUndefined();
    // The origin now errors; a neg must not be grace/SIE-served.
    const second = await cache.getOrSetEntry({
      factory: serverError,
      grace: "1h",
      key: "k",
      tags: [tag],
    });
    expect(second.servedOnError).not.toBeTruthy();
  });

  test("INV-26: a neg never displaces a servable val (E7)", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    await cache.getOrSet({
      factory: async () => await Promise.resolve("good"),
      grace: "1h",
      key: "k",
      tags: [tag],
    });
    await cache.expire({ tag }); // stale ⇒ SWR refresh
    // A transient 404 during the refresh must not replace the grace-eligible good value.
    const entry = await cache.getOrSetEntry({
      factory: notFound,
      grace: "1h",
      key: "k",
      notFoundTtl: "30s",
      tags: [tag],
    });
    expect(entry.value).toBe("good");
  });

  test.prop([commandsArb()])(
    "INV-7 INV-26 property: observed serve is a subset of legal serves under negative results",
    async (cmds) => {
      await fc.asyncModelRun(modelSetup({ isolates: 1 }), cmds);
    }
  );

  // The cached-neg RE-READ — INV-7 on the HIT turn (spec §5.1 step 5, §3).
  //
  // The tests above observe the FILL turn: the call whose factory 404'd. These observe the NEXT
  // call — the one negative caching exists for. The remembered 404 must answer it, so the origin is
  // not consulted again, and what the caller receives must be the same fact the fill surfaced. A
  // neg carries no value (§3); the bytes that remember it are a placeholder, never the answer, and
  // an answer that changes between the fill and the re-read is not a cached fact at all.
  test("INV-7: a re-read that HITS a stored neg surfaces the fact, never the stored placeholder", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    let originCalls = 0;
    const countedNotFound = async (): Promise<never> => {
      originCalls += 1;
      return await notFound();
    };
    const fill = await cache.getOrSetEntry({
      factory: countedNotFound,
      grace: "1h",
      key: "k",
      notFoundTtl: "30s",
      tags: [tag],
    });
    expect(fill.value).toBeUndefined();
    expect(originCalls).toBe(1);

    const reread = await cache.getOrSetEntry({
      factory: countedNotFound,
      grace: "1h",
      key: "k",
      notFoundTtl: "30s",
      tags: [tag],
    });
    // The neg was HIT, not refilled: the origin saw exactly one 404, and this serve came out of a
    // tier rather than the factory.
    expect(originCalls).toBe(1);
    expect(reread.tier).not.toBe("origin");
    // The fact, replayed — the answer the fill turn gave.
    expect(reread.value).toBeUndefined();
  });

  test("INV-7: serving a cached neg is neither a stale serve nor an error-path serve", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    await cache.getOrSetEntry({
      factory: notFound,
      grace: "1h",
      key: "k",
      notFoundTtl: "30s",
      tags: [tag],
    });

    const reread = await cache.getOrSetEntry({
      factory: notFound,
      grace: "1h",
      key: "k",
      notFoundTtl: "30s",
      tags: [tag],
    });
    // A neg is hard-expire only: the only one that can be served is a fresh one, and serving it is
    // not stale-if-error — the same exclusion the first test above pins on the fill turn, here on
    // the turn that hits it.
    expect(reread.stale).toBe(false);
    expect(reread.servedOnError).not.toBeTruthy();
  });

  test("INV-7: the plain form replays the same fact on a cached-neg re-read", async () => {
    const w = world({ isolates: 1 });
    const { cache } = w.isolate(0);
    const tag = mkTag("t0");
    let originCalls = 0;
    const countedNotFound = async (): Promise<never> => {
      originCalls += 1;
      return await notFound();
    };
    const filled = await cache.getOrSet({
      factory: countedNotFound,
      grace: "1h",
      key: "k",
      notFoundTtl: "30s",
      tags: [tag],
    });
    expect(filled).toBeUndefined();

    // Both API forms surface a not-found the same way (§5.1 step 5), on the fill turn and on the
    // hit turn alike — the form a caller chose decides what it learns ABOUT the serve, never what
    // the serve says the origin holds.
    const reread = await cache.getOrSet({
      factory: countedNotFound,
      grace: "1h",
      key: "k",
      notFoundTtl: "30s",
      tags: [tag],
    });
    expect(originCalls).toBe(1);
    expect(reread).toBeUndefined();
  });
});
