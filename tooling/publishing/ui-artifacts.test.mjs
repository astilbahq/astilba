import assert from "node:assert/strict";
import { test } from "node:test";

import { hash, packUiArtifacts, validateUiManifest } from "./ui-artifacts.mjs";

const fixture = () => ({
  format: "astilba.ui.release-artifacts/v1",
  packages: ["tokens", "ui"].map((name) => ({
    archive: {
      bytes: 20,
      name: `astilba-${name}-1.2.3.tgz`,
      sha256: "c".repeat(64),
    },
    directory: `packages/${name}`,
    entries: [
      {
        bytes: 1,
        mode: 420,
        path: "package/package.json",
        sha256: "d".repeat(64),
      },
    ],
    name: `@astilba/${name}`,
    version: "1.2.3",
  })),
  source: { commit: "a".repeat(40), tree: "b".repeat(40) },
  version: "1.2.3",
});

test("UI transfer manifest binds two coordinated ordered packages and immutable source", () => {
  assert.equal(validateUiManifest(fixture()).version, "1.2.3");
  for (const field of ["format", "source", "version", "packages"]) {
    assert.throws(() => validateUiManifest({ ...fixture(), [field]: null }));
  }
});

test("UI transfer manifest rejects unrelated packages, paths, versions and hashes", () => {
  for (const patch of [
    { name: "@astilba/cache" },
    { directory: "../cache" },
    { version: "2.0.0" },
    { archive: { bytes: 20, name: "../payload.tgz", sha256: "c".repeat(64) } },
    {
      archive: {
        bytes: 0,
        name: "astilba-tokens-1.2.3.tgz",
        sha256: "c".repeat(64),
      },
    },
    { archive: { bytes: 20, name: "astilba-tokens-1.2.3.tgz", sha256: "bad" } },
    { entries: [] },
  ]) {
    const value = fixture();
    value.packages[0] = { ...value.packages[0], ...patch };
    assert.throws(() => validateUiManifest(value));
  }
  assert.throws(() =>
    validateUiManifest({
      ...fixture(),
      packages: fixture().packages.toReversed(),
    })
  );
});

test("UI manifest digest changes for every altered byte", () => {
  assert.notEqual(hash("verified bytes"), hash("Verified bytes"));
});

test("retired UI rejects release packing before accessing an artifact directory", async () => {
  await assert.rejects(
    packUiArtifacts(".artifacts/retired-ui"),
    /packing is retired/u
  );
});
