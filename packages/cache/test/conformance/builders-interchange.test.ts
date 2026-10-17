// Conformance (NOT sacred), DoD completion 2. INTERCHANGEABILITY = BYTE-EQUALITY:
// a kernel-built tag (`t`) and a harness-branded tag (`mkTag`) are the SAME STRING for the same
// inputs — two construction paths, one vocabulary, no silent normalization drift. This is what
// licenses the harness's deliberate independence (branding inputs directly, off the builders):
// it holds only because neither path rewrites what the other produces.

import { t } from "@astilba/cache";
import { mkTag } from "@astilba/harness";
import { describe, expect, it } from "vitest";

import { assertUserTag } from "../../src/tags";

describe("builders — one vocabulary across construction paths (DoD-2)", () => {
  const ids = ["1", "42", "abc", "a.b-c_d", "x%20y"];

  it.each(ids)("t and mkTag agree byte-for-byte on book:%s", (id) => {
    const built = t`book:${id}`;
    const branded = mkTag(`book:${id}`);
    // Same STRING (===), not merely both-accepted — no normalization drift between paths.
    expect(built === branded).toBeTruthy();
    expect(built).toBe(`book:${id}`);
    // And treated identically by any consumer reached here: dedupe collapses them to one
    // (one vocabulary), and both clear the `__` write-boundary backstop.
    expect(new Set<string>([built, branded]).size).toBe(1);
    expect(() => {
      assertUserTag(built);
    }).not.toThrow();
    expect(() => {
      assertUserTag(branded);
    }).not.toThrow();
  });
});
