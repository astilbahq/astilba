import assert from "node:assert/strict";
import { readdir, readFile, realpath } from "node:fs/promises";
import nodePath from "node:path";
import { describe, it } from "node:test";

const { resolve } = nodePath;
const root = resolve(import.meta.dirname, "../..");
const readJson = async (path) =>
  JSON.parse(await readFile(resolve(root, path), "utf-8"));

describe("pilot boundaries", () => {
  it("keeps the public package allowlist and every other workspace private", async () => {
    const workspace = await readFile(
      resolve(root, "pnpm-workspace.yaml"),
      "utf-8"
    );
    const paths = [...workspace.matchAll(/^ {2}- (?<path>\S+)$/gmu)].map(
      (match) => match.groups.path
    );
    assert.deepEqual(paths, [
      "packages/env",
      "packages/tokens",
      "packages/ui",
      "apps/docs",
      "apps/site",
      "apps/ui-showcase",
      "fixtures/ui-astro",
    ]);
    const manifests = await Promise.all(
      paths.map((path) => readJson(`${path}/package.json`))
    );
    assert.deepEqual(
      manifests
        .filter((manifest) => manifest.private !== true)
        .map((manifest) => manifest.name)
        .toSorted(),
      ["@astilba/env", "@astilba/tokens", "@astilba/ui"]
    );
    const rootManifest = await readJson("package.json");
    assert.equal(rootManifest.private, true);
    assert.equal(manifests[1].version, manifests[2].version);
    assert.equal(workspace.includes("linkWorkspacePackages: false"), true);
  });

  it("keeps web application dependencies bound to registry releases", async () => {
    await Promise.all(
      ["apps/docs", "apps/site"].map(async (packagePath) => {
        const manifest = await readJson(`${packagePath}/package.json`);
        await Promise.all(
          ["@astilba/tokens", "@astilba/ui"].map(async (name) => {
            assert.equal(manifest.dependencies[name], "0.5.0");
            const installed = await realpath(
              resolve(root, packagePath, "node_modules", name)
            );
            const storeRelative = nodePath.relative(
              resolve(root, "node_modules/.pnpm"),
              installed
            );
            assert.notEqual(storeRelative, "");
            assert.notEqual(storeRelative, "..");
            assert.equal(storeRelative.startsWith(`..${nodePath.sep}`), false);
            assert.equal(nodePath.isAbsolute(storeRelative), false);
            const installedManifest = await readJson(
              `${packagePath}/node_modules/${name}/package.json`
            );
            assert.equal(installedManifest.version, "0.5.0");
          })
        );
      })
    );
    const docsManifest = await readJson("apps/docs/package.json");
    assert.equal(docsManifest.dependencies["@astilba/env"], "0.3.0");
  });

  it("keeps imported workflows inert and the active pilot free of publishers", async () => {
    const files = await readdir(resolve(root, ".github/workflows"));
    await Promise.all(
      files.map(async (file) => {
        const source = await readFile(
          resolve(root, ".github/workflows", file),
          "utf-8"
        );
        assert.doesNotMatch(
          source,
          /npm publish|wrangler deploy(?! --dry-run)|id-token: write|environment:|secrets\./u
        );
      })
    );
    const fixture = await readJson("fixtures/registry/env/package.json");
    assert.equal(fixture.packageManager, "pnpm@11.10.0");
    const nodeFixture = await readJson(
      "fixtures/registry/env/node-service/package.json"
    );
    assert.equal(nodeFixture.dependencies["@astilba/env"], "0.3.0");
  });
});
