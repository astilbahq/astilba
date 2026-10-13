import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import {
  mkdir,
  mkdtemp,
  readFile,
  rm,
  symlink,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import nodePath from "node:path";
import { test } from "node:test";

import { encodeManifest } from "../../packages/env/scripts/artifact-manifest.mjs";
import { cutover } from "./identity.mjs";
import { readRegistryIdentity } from "./registry-audit.mjs";

const { resolve } = nodePath;
const digest = (algorithm, bytes) =>
  createHash(algorithm).update(bytes).digest("hex");
const commit = "a".repeat(40);

const artifacts = async (directory, product) => {
  const packages = await Promise.all(
    cutover.products[product].packages.map(async (identity) => {
      const bytes = Buffer.from(
        `independently verified ${identity.name} artifact fixture`
      );
      const archive = {
        bytes: bytes.length,
        name: `astilba-${identity.name.split("/")[1]}-1.2.3.tgz`,
        sha256: digest("sha256", bytes),
      };
      await writeFile(resolve(directory, archive.name), bytes);
      return {
        ...identity,
        archive,
        entries: [
          {
            bytes: 1,
            mode: 420,
            path: "package/package.json",
            sha256: "d".repeat(64),
          },
        ],
        version: "1.2.3",
      };
    })
  );
  const source = { commit, tree: "b".repeat(40) };
  const [env] = packages;
  const manifest =
    product === "env"
      ? encodeManifest({
          archive: env.archive,
          entries: env.entries,
          format: "astilba.env.release-artifact/v1",
          package: { name: env.name, version: env.version },
          source,
        })
      : JSON.stringify({
          format: "astilba.ui.release-artifacts/v1",
          packages,
          source,
          version: "1.2.3",
        });
  await writeFile(resolve(directory, "manifest.json"), manifest);
  return packages;
};

for (const product of ["env", "ui"]) {
  test(`${product} derives provenance expectations from local archive bytes, not registry metadata`, async () => {
    const directory = await mkdtemp(
      resolve(tmpdir(), "astilba-registry-identity-")
    );
    try {
      const packages = await artifacts(directory, product);
      const expected = await readRegistryIdentity(
        product,
        `${product}/v1.2.3`,
        commit,
        directory
      );
      assert.deepEqual(expected, {
        commit,
        packages: packages.map((p) => ({
          name: p.name,
          sha512: digest(
            "sha512",
            `independently verified ${p.name} artifact fixture`
          ),
          version: p.version,
        })),
      });
      await assert.rejects(
        readRegistryIdentity(
          product,
          `${product}/v1.2.3`,
          "c".repeat(40),
          directory
        ),
        /artifact commit/u
      );
      await assert.rejects(
        readRegistryIdentity(product, `${product}/v1.2.4`, commit, directory),
        /Expected identity/u
      );
      const [entry] = packages;
      const archive = resolve(directory, entry.archive.name);
      const bytes = await readFile(archive);
      await writeFile(archive, Buffer.alloc(bytes.length, 1));
      await assert.rejects(
        readRegistryIdentity(product, `${product}/v1.2.3`, commit, directory),
        /archive bytes/u
      );
      await writeFile(archive, bytes);
      await rm(archive);
      await symlink(resolve(directory, "manifest.json"), archive);
      await assert.rejects(
        readRegistryIdentity(product, `${product}/v1.2.3`, commit, directory),
        /regular file/u
      );
    } finally {
      await rm(directory, { force: true, recursive: true });
    }
  });
}

test("artifact identity rejects symlinked directories and manifests", async () => {
  const directory = await mkdtemp(resolve(tmpdir(), "astilba-registry-links-"));
  try {
    const target = resolve(directory, "target");
    await mkdir(target);
    await artifacts(target, "env");
    const link = resolve(directory, "link");
    await symlink(target, link);
    await assert.rejects(
      readRegistryIdentity("env", "env/v1.2.3", commit, link),
      /real directory/u
    );
    const file = resolve(target, "manifest.json");
    const bytes = await readFile(file);
    await writeFile(resolve(directory, "manifest.json"), bytes);
    await rm(file);
    await symlink(resolve(directory, "manifest.json"), file);
    await assert.rejects(
      readRegistryIdentity("env", "env/v1.2.3", commit, target),
      /regular file/u
    );
  } finally {
    await rm(directory, { force: true, recursive: true });
  }
});
