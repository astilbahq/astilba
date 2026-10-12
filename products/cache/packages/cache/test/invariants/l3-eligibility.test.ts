// INV-13 — spec §11.13, §7.2
// Sacred: append-only. Never modify, delete, weaken, skip, or .todo an existing test.
// Uses the render-time collector as the stub frame-collector/emission adapter.

import { mkTag, world } from "@astilba/harness";
import { test } from "@fast-check/vitest";
import fc from "fast-check";
import { describe, expect } from "vitest";

describe("L3 eligibility", () => {
  test("INV-13: required tags exceeding the L3 budget make the response L3-ineligible", () => {
    const collector = world({ isolates: 1 }).isolate(0).cache.collect();
    for (let i = 0; i < 10; i++) {
      collector.dependsOn(mkTag(`t${i}`));
    }
    const emission = collector.commitHeaders({ maxTags: 3 });
    expect(emission.eligible).toBeFalsy();
    expect(emission.ineligibleReason).toBe("budget");
  });

  test("INV-13: a required tag discovered after header commit is L3-ineligible (late-tag)", () => {
    const collector = world({ isolates: 1 }).isolate(0).cache.collect();
    collector.dependsOn(mkTag("t0"));
    collector.commitHeaders(); // headers commit here
    collector.dependsOn(mkTag("t-late")); // discovered after commit
    const finalEmission = collector.commitHeaders();
    expect(finalEmission.eligible).toBeFalsy();
    expect(finalEmission.ineligibleReason).toBe("late-tag");
  });

  test("INV-13: all required tags known before commit and within budget are eligible", () => {
    const collector = world({ isolates: 1 }).isolate(0).cache.collect();
    collector.dependsOn(mkTag("t0"));
    collector.dependsOn(mkTag("t1"));
    const emission = collector.commitHeaders({ maxTags: 100 });
    expect(emission.eligible).toBeTruthy();
    expect(emission.cacheTags).toContain("t0");
  });

  test.prop([
    fc.array(fc.string({ minLength: 1 }), { maxLength: 30 }),
    fc.integer({ max: 20, min: 1 }),
  ])(
    "INV-13 property: an over-budget required-tag set is never L3-eligible",
    (tags, budget) => {
      const collector = world({ isolates: 1 }).isolate(0).cache.collect();
      for (const tag of tags) {
        collector.dependsOn(mkTag(tag));
      }
      const emission = collector.commitHeaders({ maxTags: budget });
      if (tags.length > budget) {
        expect(emission.eligible).toBeFalsy();
      }
    }
  );

  // INV-13 — spec §7.2
  // The budget measures OCCURRENCES, not distinct tags. A render consumes an entry once per read,
  // so a tag reaches the collector once per consumption, and the property above already implies it
  // (its over-budget premise is `tags.length > budget`, a count of occurrences) — but its generator
  // reaches a duplicate-heavy over-budget list only by chance, so the measure is pinned in name and
  // unguarded in fact. This is that measure as an example that always has the shape: four
  // occurrences of ONE tag against a budget of three. Collapsing the repeats before counting would
  // admit it, which is the fail-OPEN direction — an over-budget header going out eligible.
  test("INV-13: duplicate required tags count once EACH — four occurrences of one tag exceed a budget of three", () => {
    const collector = world({ isolates: 1 }).isolate(0).cache.collect();
    for (let i = 0; i < 4; i++) {
      collector.dependsOn(mkTag("dup")); // the SAME tag, four times over
    }
    const emission = collector.commitHeaders({ maxTags: 3 });
    expect(emission.eligible).toBeFalsy();
    expect(emission.ineligibleReason).toBe("budget");

    // CONTROL, so the case cannot pass for the wrong reason: the very same four occurrences are
    // eligible at a budget of four. It is the COUNT that is over, not the tag that is forbidden.
    const control = world({ isolates: 1 }).isolate(0).cache.collect();
    for (let i = 0; i < 4; i++) {
      control.dependsOn(mkTag("dup"));
    }
    expect(control.commitHeaders({ maxTags: 4 }).eligible).toBeTruthy();
  });
});
