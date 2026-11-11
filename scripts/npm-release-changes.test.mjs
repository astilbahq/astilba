import assert from "node:assert/strict";
import test from "node:test";
import {
  addedVersions,
  concerns,
  lockedVersions,
  newestFirst,
} from "./npm-release-changes.mjs";

const lockfile = (entries) => `lockfileVersion: '9.0'

importers:

  .:
    devDependencies:
      pnpm:
        specifier: 'catalog:'
        version: 11.17.0

packages:

${entries.map((key) => `  ${key}:\n    resolution: {integrity: sha512-a}\n`).join("\n")}
snapshots:

  pnpm@11.28.11: {}
`;

test("only the packages section counts, scoped names included", () => {
  const versions = lockedVersions(
    lockfile(["'@swc/core@1.16.20'", "esbuild@0.28.2", "esbuild@0.27.0"]),
  );
  assert.deepEqual(
    [...versions].map(([name, set]) => [name, [...set]]),
    [
      ["@swc/core", ["1.16.20"]],
      ["esbuild", ["0.28.2", "0.27.0"]],
    ],
  );
});

test("release numbers compare numerically", () => {
  assert.deepEqual(
    ["1.9.0", "1.10.0", "1.10.0-beta.1", "0.28.2"].sort(newestFirst),
    ["1.10.0", "1.10.0-beta.1", "1.9.0", "0.28.2"],
  );
});

test("an added version sits beside the newest version the base locked", () => {
  const base = lockedVersions(
    lockfile(["'@swc/core@1.16.20'", "esbuild@0.27.0", "esbuild@0.28.2"]),
  );
  const current = lockedVersions(
    lockfile(["'@swc/core@1.16.21'", "esbuild@0.28.2", "nano-spawn@2.0.0"]),
  );
  assert.deepEqual(addedVersions(base, current), [
    { name: "@swc/core", version: "1.16.21", previous: "1.16.20" },
    { name: "nano-spawn", version: "2.0.0", previous: undefined },
  ]);
});

const manifest = (fields) => ({
  _npmUser: { name: "alice" },
  maintainers: [{ name: "alice" }],
  dist: { attestations: { url: "https://example.test" } },
  ...fields,
});

test("an unchanged publisher, maintainer list and install surface raise nothing", () => {
  assert.deepEqual(concerns(manifest({}), manifest({})), []);
});

test("a new install script, publisher, maintainer or missing provenance is raised", () => {
  assert.deepEqual(
    concerns(
      manifest({
        _npmUser: { name: "mallory" },
        maintainers: [{ name: "alice" }, { name: "mallory" }],
        scripts: { postinstall: "node setup.js", test: "vitest" },
        dist: {},
      }),
      manifest({}),
    ),
    [
      "runs postinstall on install",
      "published by mallory, previously alice",
      "new maintainers mallory",
      "no provenance, which the previous version had",
    ],
  );
});

test("an install script the previous version already ran is not raised again", () => {
  const scripts = { scripts: { install: "node-gyp rebuild" }, gypfile: true };
  assert.deepEqual(concerns(manifest(scripts), manifest(scripts)), []);
});

test("a new package lists its maintainers and install scripts", () => {
  assert.deepEqual(
    concerns(manifest({ gypfile: true, deprecated: "use other" }), undefined),
    [
      "runs binding.gyp on install",
      "new package, maintained by alice",
      "deprecated: use other",
    ],
  );
});
