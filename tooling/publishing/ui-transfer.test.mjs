import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import {
  chmod,
  cp,
  lstat,
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

import { cutover, root } from "./identity.mjs";
import { hash, inspectUiArchive, verifyUiArtifacts } from "./ui-artifacts.mjs";

const { resolve } = nodePath;
const git = (ref) =>
  execFileSync("git", ["rev-parse", ref], {
    cwd: root,
    encoding: "utf-8",
  }).trim();

const transferFixture = async (directory) => {
  const artifacts = resolve(directory, "artifacts");
  await mkdir(artifacts);
  const packages = await Promise.all(
    cutover.products.ui.packages.map(async (identity) => {
      const source = JSON.parse(
        await readFile(
          resolve(root, identity.directory, "package.json"),
          "utf-8"
        )
      );
      delete source.packageManager;
      if (identity.name === "@astilba/ui") {
        source.dependencies["@astilba/tokens"] = source.version;
      }
      const staging = resolve(directory, identity.name.split("/")[1]);
      await mkdir(staging);
      await writeFile(resolve(staging, "package.json"), JSON.stringify(source));
      const name = `astilba-${identity.name.split("/")[1]}-${source.version}.tgz`;
      execFileSync(
        "npm",
        [
          "pack",
          "--offline",
          "--ignore-scripts",
          "--json",
          "--pack-destination",
          artifacts,
        ],
        { cwd: staging }
      );
      const bytes = execFileSync("tar", [
        "-xOzf",
        resolve(artifacts, name),
        "package/package.json",
      ]);
      const archive = await readFile(resolve(artifacts, name));
      return {
        ...identity,
        archive: { bytes: archive.length, name, sha256: hash(archive) },
        entries: [
          {
            bytes: bytes.length,
            mode: 420,
            path: "package/package.json",
            sha256: hash(bytes),
          },
        ],
        version: source.version,
      };
    })
  );
  const manifest = {
    format: "astilba.ui.release-artifacts/v1",
    packages,
    source: { commit: git("HEAD"), tree: git("HEAD^{tree}") },
    version: packages[0].version,
  };
  const bytes = Buffer.from(`${JSON.stringify(manifest, null, 2)}\n`);
  await writeFile(resolve(artifacts, "manifest.json"), bytes);
  await writeFile(
    resolve(artifacts, "SHA256SUMS"),
    packages.map((p) => `${p.archive.sha256}  ${p.archive.name}\n`).join("")
  );
  return { artifacts, digest: hash(bytes), manifest };
};

test("UI transfer verifies archive bytes and rejects independent corruption controls", async () => {
  const directory = await mkdtemp(resolve(tmpdir(), "astilba-ui-transfer-"));
  try {
    const fixture = await transferFixture(directory);
    const result = await verifyUiArtifacts(fixture.artifacts, fixture.digest);
    assert.deepEqual(result, fixture.manifest);
    const corruptions = [
      (copy) =>
        writeFile(
          resolve(copy, fixture.manifest.packages[0].archive.name),
          "different bytes"
        ),
      (copy) => writeFile(resolve(copy, "manifest.json"), "{}"),
      (copy) => writeFile(resolve(copy, "SHA256SUMS"), "wrong checksum"),
      (copy) => writeFile(resolve(copy, "unexpected.tgz"), "extra archive"),
      async (copy) => {
        await rm(resolve(copy, "SHA256SUMS"));
        await symlink(
          resolve(fixture.artifacts, "SHA256SUMS"),
          resolve(copy, "SHA256SUMS")
        );
      },
    ];
    await Promise.all(
      corruptions.map(async (corrupt, index) => {
        const copy = resolve(directory, `corrupt-${index}`);
        await cp(fixture.artifacts, copy, { recursive: true });
        await corrupt(copy);
        await assert.rejects(verifyUiArtifacts(copy, fixture.digest));
      })
    );
    await assert.rejects(verifyUiArtifacts(fixture.artifacts, "0".repeat(64)));
    const changed = structuredClone(fixture.manifest);
    changed.source.commit = "f".repeat(40);
    const changedBytes = Buffer.from(JSON.stringify(changed));
    await writeFile(resolve(fixture.artifacts, "manifest.json"), changedBytes);
    await assert.rejects(
      verifyUiArtifacts(fixture.artifacts, hash(changedBytes)),
      /committed checkout/u
    );
  } finally {
    await rm(directory, { force: true, recursive: true });
  }
});

test("UI archive inspector rejects a link before extraction and wrong executable modes", async () => {
  const directory = await mkdtemp(resolve(tmpdir(), "astilba-ui-archive-"));
  try {
    await mkdir(resolve(directory, "package"));
    const target = resolve(directory, "target");
    await writeFile(target, "private sentinel stays outside the archive");
    await symlink(target, resolve(directory, "package/linked"));
    const archive = resolve(directory, "linked.tgz");
    execFileSync("tar", ["-czf", archive, "-C", directory, "package/linked"]);
    assert.throws(
      () => inspectUiArchive("packages/tokens", archive),
      /non-regular/u
    );
    const targetInfo = await lstat(target);
    assert.equal(targetInfo.isFile(), true);
    assert.equal(
      await readFile(target, "utf-8"),
      "private sentinel stays outside the archive"
    );
    const executable = resolve(directory, "package/executable");
    await writeFile(executable, "unexpected executable");
    await chmod(executable, 0o755);
    const metadata = Buffer.from(
      '{"name":"@astilba/tokens","version":"0.6.0"}'
    );
    await writeFile(resolve(directory, "package/package.json"), metadata);
    await chmod(resolve(directory, "package/package.json"), 0o644);
    const badMode = resolve(directory, "mode.tgz");
    execFileSync("tar", [
      ...(process.platform === "darwin"
        ? ["--uid", "0", "--gid", "0", "--uname", "", "--gname", ""]
        : []),
      "-czf",
      badMode,
      "-C",
      directory,
      "package/executable",
      "package/package.json",
    ]);
    await assert.rejects(
      inspectUiArchive("packages/tokens", badMode, [
        {
          bytes: 21,
          mode: 493,
          path: "package/executable",
          sha256: hash("unexpected executable"),
        },
        {
          bytes: metadata.length,
          mode: 420,
          path: "package/package.json",
          sha256: hash(metadata),
        },
      ]),
      /Release archive file metadata is invalid/u
    );
  } finally {
    await rm(directory, { force: true, recursive: true });
  }
});
