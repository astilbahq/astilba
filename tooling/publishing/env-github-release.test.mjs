import assert from "node:assert/strict";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import nodePath from "node:path";
import { test } from "node:test";

import { encodeManifest } from "../../packages/env/scripts/artifact-manifest.mjs";
import {
  planNamespacedEnvRelease,
  verifyNamespacedEnvRelease,
} from "./env-github-release.mjs";
import { hash } from "./ui-artifacts.mjs";

const { join } = nodePath;

test("namespaced Env release keeps legacy exact assets and retry verification", async () => {
  const directory = await mkdtemp(join(tmpdir(), "astilba-env-namespaced-"));
  try {
    const artifactDirectory = join(directory, "artifact");
    const downloadedDirectory = join(directory, "downloaded");
    await Promise.all([mkdir(artifactDirectory), mkdir(downloadedDirectory)]);
    const archive = Buffer.from("verified archive");
    const archiveName = "astilba-env-1.2.3.tgz";
    const manifest = Buffer.from(
      encodeManifest({
        archive: {
          bytes: archive.length,
          name: archiveName,
          sha256: hash(archive),
        },
        entries: [
          {
            bytes: 1,
            mode: 420,
            path: "package/index.js",
            sha256: "c".repeat(64),
          },
        ],
        format: "astilba.env.release-artifact/v1",
        package: { name: "@astilba/env", version: "1.2.3" },
        source: { commit: "a".repeat(40), tree: "b".repeat(40) },
      })
    );
    await Promise.all(
      [artifactDirectory, downloadedDirectory].flatMap((folder) => [
        writeFile(join(folder, archiveName), archive),
        writeFile(join(folder, "manifest.json"), manifest),
      ])
    );
    const release = {
      assets: [
        { id: 2, name: archiveName, size: archive.length, state: "uploaded" },
        {
          id: 3,
          name: "manifest.json",
          size: manifest.length,
          state: "uploaded",
        },
      ],
      draft: false,
      id: 1,
      prerelease: false,
      tag_name: "env/v1.2.3",
    };
    const options = {
      artifactDirectory,
      expectedTag: release.tag_name,
      releaseSource: JSON.stringify(release),
    };
    const plan = await planNamespacedEnvRelease(options);
    assert.equal(plan.tag, release.tag_name);
    const verification = await verifyNamespacedEnvRelease({
      ...options,
      afterSource: options.releaseSource,
      beforeSource: options.releaseSource,
      downloadedDirectory,
    });
    assert.equal(verification.passed, true);
    await Promise.all(
      ["v1.2.3", "ui/v1.2.3", "env/v1.2.4"].map((tag) =>
        assert.rejects(
          planNamespacedEnvRelease({ ...options, expectedTag: tag })
        )
      )
    );
    await assert.rejects(
      planNamespacedEnvRelease({
        ...options,
        releaseSource: JSON.stringify({
          ...release,
          assets: release.assets.slice(0, 1),
        }),
      })
    );
    await writeFile(
      join(downloadedDirectory, archiveName),
      Buffer.from("tampered archive")
    );
    await assert.rejects(
      verifyNamespacedEnvRelease({
        ...options,
        afterSource: options.releaseSource,
        beforeSource: options.releaseSource,
        downloadedDirectory,
      })
    );
  } finally {
    await rm(directory, { force: true, recursive: true });
  }
});
