import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

import {
  cutover,
  publisherMetadata,
  releaseVersion,
  root,
  verifyIdentity,
} from "./identity.mjs";

const fixture = (product) => ({
  commit: "a".repeat(40),
  currentMain: "a".repeat(40),
  manifests: cutover.products[product].packages.map(({ name, directory }) => ({
    name,
    version: "1.2.3",
    ...publisherMetadata(directory),
    publishConfig: { access: "public", provenance: true },
  })),
  product,
  repository: "astilbahq/astilba",
  tag: `${product}/v1.2.3`,
  tagCommit: "a".repeat(40),
});

for (const product of ["env", "ui"]) {
  test(`${product} accepts only its exact stable namespace and main identity`, () => {
    assert.equal(verifyIdentity(fixture(product)).version, "1.2.3");
    for (const field of ["commit", "currentMain", "tagCommit"]) {
      assert.throws(() =>
        verifyIdentity({ ...fixture(product), [field]: "b".repeat(40) })
      );
    }
    assert.throws(() =>
      verifyIdentity({ ...fixture(product), repository: "astilbahq/env" })
    );
  });

  test(`${product} rejects legacy, cross-product, malformed and prerelease tags`, () => {
    for (const tag of [
      "v1.2.3",
      "cache/v1.2.3",
      `${product}/v01.2.3`,
      `${product}/v1.2.3-rc.1`,
      `${product}/v1.2.3+build`,
      `${product}/v1.2.3/other`,
      `${product}/v1.2`,
    ]) {
      assert.throws(() => releaseVersion(product, tag));
    }
    assert.throws(() =>
      releaseVersion(product, `${product === "env" ? "ui" : "env"}/v1.2.3`)
    );
  });

  test(`${product} rejects package, version, provenance and repository drift`, () => {
    for (const patch of [
      { name: "@astilba/cache" },
      { version: "1.2.4" },
      { private: true },
      { publishConfig: { access: "public", provenance: false } },
      {
        repository: {
          type: "git",
          url: "git+https://github.com/astilbahq/ui.git",
        },
      },
      { bugs: { url: "https://github.com/astilbahq/env/issues" } },
      { homepage: "https://example.com" },
    ]) {
      const options = fixture(product);
      options.manifests[0] = { ...options.manifests[0], ...patch };
      assert.throws(() => verifyIdentity(options));
    }
    assert.throws(() => verifyIdentity({ ...fixture(product), manifests: [] }));
  });

  test(`${product} cannot publish the still-legacy metadata in this preparation`, async () => {
    const manifests = await Promise.all(
      cutover.products[product].packages.map(async ({ directory }) =>
        JSON.parse(await readFile(`${root}/${directory}/package.json`, "utf-8"))
      )
    );
    const options = fixture(product);
    options.tag = `${product}/v${manifests[0].version}`;
    options.manifests = manifests;
    assert.throws(() => verifyIdentity(options), /metadata is not activated/u);
  });
}

test("Cache never acquires a namespace, package set or trust binding", () => {
  assert.deepEqual(Object.keys(cutover.products).toSorted(), ["env", "ui"]);
  assert.throws(() => releaseVersion("cache", "cache/v1.2.3"));
});
