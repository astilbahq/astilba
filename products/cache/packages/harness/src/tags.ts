import type { Tag } from "@astilba/cache";

// Test-input tag construction — DELIBERATE INDEPENDENCE, not a workaround. Sacred/driver
// test inputs are branded here directly, INDEPENDENTLY of the kernel builders (`t` now
// validates-and-rejects, `compound` escapes; `globalTag` is still a stub), by design: the
// harness must mint arbitrary tags to drive commands, and — the load-bearing reason — a
// defect in a builder must red only its OWN teeth (test/unit + test/conformance), never
// silently corrupt the inputs of 73 unrelated invariants. This is a single narrowing
// assertion (string → branded string), not an `as unknown as` cast.

/** Brand a raw string as a `Tag` for test inputs (NOT the kernel's `t`). */
export function mkTag(raw: string): Tag {
  // oxlint-disable-next-line typescript/no-unsafe-type-assertion -- controlled fixture input receives the tag brand without production validation
  return raw as Tag;
}

/** Brand a canonical key string. */
export function mkKey(raw: string): string {
  return raw;
}
