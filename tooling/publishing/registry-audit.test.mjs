import assert from "node:assert/strict";
import { test } from "node:test";

import { cutover } from "./identity.mjs";
import { verifyRegistryAudit } from "./registry-audit.mjs";

const fixture = (product) => ({
  invalid: [],
  missing: [],
  verified: cutover.products[product].packages.map(({ name }) => ({
    attestationBundles: [
      { predicateType: "https://slsa.dev/provenance/v1" },
      {
        predicateType:
          "https://github.com/npm/attestation/tree/main/specs/publish/v0.1",
      },
    ],
    attestations: {
      provenance: { predicateType: "https://slsa.dev/provenance/v1" },
      url: `https://registry.npmjs.org/-/npm/v1/attestations/${encodeURIComponent(name)}@1.2.3`,
    },
    name,
    version: "1.2.3",
  })),
});

for (const product of ["env", "ui"]) {
  test(`${product} requires verified npm signatures and both attestation kinds for its exact packages`, () => {
    assert.equal(
      verifyRegistryAudit(fixture(product), product, `${product}/v1.2.3`)
        .passed,
      true
    );
    for (const field of ["invalid", "missing"]) {
      assert.throws(() =>
        verifyRegistryAudit(
          { ...fixture(product), [field]: [{}] },
          product,
          `${product}/v1.2.3`
        )
      );
    }
    for (const patch of [
      { version: "1.2.4" },
      { name: "@astilba/cache" },
      { attestationBundles: [] },
      { attestations: { url: "https://example.com/payload" } },
    ]) {
      const audit = fixture(product);
      audit.verified[0] = { ...audit.verified[0], ...patch };
      assert.throws(() =>
        verifyRegistryAudit(audit, product, `${product}/v1.2.3`)
      );
    }
    const duplicate = fixture(product);
    duplicate.verified.push(duplicate.verified[0]);
    assert.throws(() =>
      verifyRegistryAudit(duplicate, product, `${product}/v1.2.3`)
    );
  });
}
