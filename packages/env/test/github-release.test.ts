import { createHash } from "node:crypto";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { afterEach, describe, expect, it } from "vitest";

type BuildReleasePlan = (options: {
  artifactDirectory: string;
  expectedTag: string;
  releaseSource: string;
}) => Promise<{
  assets: { id: number; name: string; size: number; state: "uploaded" }[];
  releaseId: number;
  tag: string;
}>;

type VerifyReleaseEvidence = (options: {
  afterSource: string;
  artifactDirectory: string;
  beforeSource: string;
  downloadedDirectory: string;
  expectedTag: string;
}) => Promise<{
  assets: string[];
  passed: true;
  releaseId: number;
  tag: string;
}>;

const isBuildReleasePlan = (value: unknown): value is BuildReleasePlan =>
  typeof value === "function";

const isVerifyReleaseEvidence = (
  value: unknown
): value is VerifyReleaseEvidence => typeof value === "function";

const githubReleaseModule: unknown =
  // @ts-expect-error -- The verifier is a checked JSDoc module without declarations.
  await import("../scripts/github-release.mjs");
if (
  typeof githubReleaseModule !== "object" ||
  githubReleaseModule === null ||
  !("buildReleasePlan" in githubReleaseModule) ||
  !isBuildReleasePlan(githubReleaseModule.buildReleasePlan) ||
  !("verifyReleaseEvidence" in githubReleaseModule) ||
  !isVerifyReleaseEvidence(githubReleaseModule.verifyReleaseEvidence)
) {
  throw new TypeError("GitHub release verifier exports are unavailable.");
}
const { buildReleasePlan, verifyReleaseEvidence } = githubReleaseModule;

const temporaryDirectories: string[] = [];

const sha256 = (value: Uint8Array | string) =>
  createHash("sha256").update(value).digest("hex");

const createFixture = async () => {
  const directory = await mkdtemp(join(tmpdir(), "astilba-env-release-"));
  temporaryDirectories.push(directory);
  const artifactDirectory = join(directory, "artifact");
  const downloadedDirectory = join(directory, "downloaded");
  await mkdir(artifactDirectory);
  await mkdir(downloadedDirectory);

  const archiveName = "astilba-env-0.2.0.tgz";
  const archive = Buffer.from("verified archive");
  const manifest = {
    archive: {
      bytes: archive.byteLength,
      name: archiveName,
      sha256: sha256(archive),
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
    package: { name: "@astilba/env", version: "0.2.0" },
    source: { commit: "a".repeat(40), tree: "b".repeat(40) },
  };
  const manifestBytes = Buffer.from(`${JSON.stringify(manifest, null, 2)}\n`);
  await writeFile(join(artifactDirectory, archiveName), archive);
  await writeFile(join(artifactDirectory, "manifest.json"), manifestBytes);
  await writeFile(join(downloadedDirectory, archiveName), archive);
  await writeFile(join(downloadedDirectory, "manifest.json"), manifestBytes);

  const release = {
    assets: [
      {
        id: 102,
        name: "manifest.json",
        size: manifestBytes.byteLength,
        state: "uploaded",
      },
      {
        id: 101,
        name: archiveName,
        size: archive.byteLength,
        state: "uploaded",
      },
    ],
    draft: false,
    id: 100,
    prerelease: false,
    tag_name: "v0.2.0",
  };
  return {
    archiveName,
    archiveSize: archive.byteLength,
    artifactDirectory,
    downloadedDirectory,
    manifestSize: manifestBytes.byteLength,
    release,
  };
};

describe("GitHub release evidence", () => {
  afterEach(async () => {
    for (const directory of temporaryDirectories.splice(0)) {
      await rm(directory, { force: true, recursive: true });
    }
  });

  it("plans and verifies an exact stable release", async () => {
    const fixture = await createFixture();
    const releaseSource = JSON.stringify(fixture.release);
    const plan = await buildReleasePlan({
      artifactDirectory: fixture.artifactDirectory,
      expectedTag: "v0.2.0",
      releaseSource,
    });
    expect(plan).toStrictEqual({
      assets: [
        {
          id: 101,
          name: fixture.archiveName,
          size: fixture.archiveSize,
          state: "uploaded",
        },
        {
          id: 102,
          name: "manifest.json",
          size: fixture.manifestSize,
          state: "uploaded",
        },
      ],
      releaseId: 100,
      tag: "v0.2.0",
    });
    await expect(
      verifyReleaseEvidence({
        afterSource: releaseSource,
        artifactDirectory: fixture.artifactDirectory,
        beforeSource: releaseSource,
        downloadedDirectory: fixture.downloadedDirectory,
        expectedTag: "v0.2.0",
      })
    ).resolves.toStrictEqual({
      assets: [fixture.archiveName, "manifest.json"],
      passed: true,
      releaseId: 100,
      tag: "v0.2.0",
    });
  });

  it("rejects a draft, prerelease, wrong tag, or non-exact asset set", async () => {
    const fixture = await createFixture();
    const invalidReleases = [
      { ...fixture.release, draft: true },
      { ...fixture.release, prerelease: true },
      { ...fixture.release, tag_name: "v0.2.1" },
      {
        ...fixture.release,
        assets: [
          ...fixture.release.assets,
          {
            id: 103,
            name: "extra.txt",
            size: 1,
            state: "uploaded",
          },
        ],
      },
    ];
    for (const release of invalidReleases) {
      await expect(
        buildReleasePlan({
          artifactDirectory: fixture.artifactDirectory,
          expectedTag: "v0.2.0",
          releaseSource: JSON.stringify(release),
        })
      ).rejects.toThrow(/GitHub release/u);
    }
  });

  it("rejects a partially created release for manual correction", async () => {
    const fixture = await createFixture();
    const partialRelease = {
      ...fixture.release,
      assets: fixture.release.assets.slice(0, 1),
    };
    await expect(
      buildReleasePlan({
        artifactDirectory: fixture.artifactDirectory,
        expectedTag: "v0.2.0",
        releaseSource: JSON.stringify(partialRelease),
      })
    ).rejects.toThrow(
      "GitHub release state does not match the requested release."
    );
  });

  it("rejects release mutation across the download boundary", async () => {
    const fixture = await createFixture();
    const changed = structuredClone(fixture.release);
    const firstAsset = changed.assets[0];
    if (firstAsset === undefined) {
      throw new TypeError("Expected fixture release assets.");
    }
    changed.assets[0] = { ...firstAsset, id: 999 };
    await expect(
      verifyReleaseEvidence({
        afterSource: JSON.stringify(changed),
        artifactDirectory: fixture.artifactDirectory,
        beforeSource: JSON.stringify(fixture.release),
        downloadedDirectory: fixture.downloadedDirectory,
        expectedTag: "v0.2.0",
      })
    ).rejects.toThrow("GitHub release changed while its assets were verified.");
  });

  it("rejects missing, extra, or byte-mismatched downloads", async () => {
    const mismatchedFixture = await createFixture();
    const mismatchedReleaseSource = JSON.stringify(mismatchedFixture.release);
    await writeFile(
      join(
        mismatchedFixture.downloadedDirectory,
        mismatchedFixture.archiveName
      ),
      "mismatch"
    );
    await expect(
      verifyReleaseEvidence({
        afterSource: mismatchedReleaseSource,
        artifactDirectory: mismatchedFixture.artifactDirectory,
        beforeSource: mismatchedReleaseSource,
        downloadedDirectory: mismatchedFixture.downloadedDirectory,
        expectedTag: "v0.2.0",
      })
    ).rejects.toThrow("Downloaded GitHub release asset bytes do not match.");

    const missingFixture = await createFixture();
    const missingReleaseSource = JSON.stringify(missingFixture.release);
    await rm(join(missingFixture.downloadedDirectory, "manifest.json"));
    await expect(
      verifyReleaseEvidence({
        afterSource: missingReleaseSource,
        artifactDirectory: missingFixture.artifactDirectory,
        beforeSource: missingReleaseSource,
        downloadedDirectory: missingFixture.downloadedDirectory,
        expectedTag: "v0.2.0",
      })
    ).rejects.toThrow("Downloaded GitHub release asset set is not exact.");

    const extraFixture = await createFixture();
    const extraReleaseSource = JSON.stringify(extraFixture.release);
    await writeFile(
      join(extraFixture.downloadedDirectory, "extra.txt"),
      "extra"
    );
    await expect(
      verifyReleaseEvidence({
        afterSource: extraReleaseSource,
        artifactDirectory: extraFixture.artifactDirectory,
        beforeSource: extraReleaseSource,
        downloadedDirectory: extraFixture.downloadedDirectory,
        expectedTag: "v0.2.0",
      })
    ).rejects.toThrow("Downloaded GitHub release asset set is not exact.");
  });
});
