import assert from "node:assert/strict";
import { readdir, readFile, realpath } from "node:fs/promises";
import nodePath from "node:path";
import { describe, it } from "node:test";

import { parse } from "yaml";

const { resolve } = nodePath;
const root = resolve(import.meta.dirname, "../..");
const readJson = async (path) =>
  JSON.parse(await readFile(resolve(root, path), "utf-8"));

describe("pilot boundaries", () => {
  it("routes Env metadata to the monorepo while preserving UI publisher identities", async () => {
    const manifest = await readJson("package.json");
    assert.equal(manifest.name, "astilba");
    assert.equal(manifest.private, true);
    assert.equal(
      manifest.repository.url,
      "git+https://github.com/astilbahq/astilba.git"
    );
    assert.equal(
      manifest.bugs.url,
      "https://github.com/astilbahq/astilba/issues"
    );
    assert.equal(
      manifest.homepage,
      "https://github.com/astilbahq/astilba#readme"
    );
    await Promise.all(
      [
        ["packages/env", "astilba"],
        ["packages/tokens", "ui"],
        ["packages/ui", "ui"],
      ].map(async ([path, repository]) => {
        const published = await readJson(`${path}/package.json`);
        assert.equal(
          published.repository.url,
          `git+https://github.com/astilbahq/${repository}.git`
        );
      })
    );
    const security = await readFile(resolve(root, "SECURITY.md"), "utf-8");
    assert.match(
      security,
      /https:\/\/github\.com\/astilbahq\/astilba\/security\/advisories\/new/u
    );
    const issueChooser = parse(
      await readFile(
        resolve(root, ".github/ISSUE_TEMPLATE/config.yml"),
        "utf-8"
      )
    );
    assert.equal(
      issueChooser.contact_links.find(
        (link) => link.name === "Report a security vulnerability"
      )?.url,
      "https://github.com/astilbahq/astilba/security/advisories/new"
    );
  });

  it("preserves public package metadata during the development-only cutover", async () => {
    // Update this reviewed baseline only alongside an approved public-contract change.
    const expected = await readJson(
      "tooling/migration/public-package-metadata.json"
    );
    assert.deepEqual(Object.keys(expected).toSorted(), [
      "@astilba/tokens",
      "@astilba/ui",
    ]);
    await Promise.all(
      ["packages/tokens", "packages/ui"].map(async (packagePath) => {
        const published = await readJson(`${packagePath}/package.json`);
        const metadata = Object.fromEntries(
          Object.entries(published).filter(
            ([key]) => !["scripts", "devDependencies"].includes(key)
          )
        );
        assert.deepEqual(metadata, expected[published.name]);
      })
    );
  });

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
      "products/cache",
      "products/cache/packages/cache",
      "products/cache/packages/harness",
      "products/cache/apps/demo",
      "products/cache/apps/rr-fixture",
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

  it("runs Cache under one root install and the active workflow census", async () => {
    const cache = await readJson("products/cache/package.json");
    assert.equal(cache.packageManager, undefined);
    assert.match(cache.scripts.check, /--type-aware --type-check/u);
    const wrangler = await readJson(
      "products/cache/packages/cache/node_modules/wrangler/package.json"
    );
    assert.equal(wrangler.version, "4.109.0");
    const paths = await readdir(resolve(root, "products/cache"));
    assert.equal(paths.includes("pnpm-lock.yaml"), false);
    assert.equal(paths.includes("pnpm-workspace.yaml"), false);
    const manifest = await readJson("package.json");
    assert.match(manifest.scripts.verify, /pnpm verify:cache/u);
    const workflow = parse(
      await readFile(resolve(root, ".github/workflows/cache.yml"), "utf-8")
    );
    const census = parse(
      await readFile(
        resolve(root, "products/cache/.github/workflows/ci.yml"),
        "utf-8"
      )
    );
    assert.deepEqual(workflow, census);
    const lanes = Object.keys(workflow.jobs)
      .filter((name) => name !== "required")
      .toSorted();
    const expectedLanes = [
      "check",
      "invariants",
      "sacred-unwritable-by-tools",
      "conformance",
      "integration",
      "worker-types-drift",
      "bun",
      "knip",
      "build-smoke",
      "boot-smoke",
      "module-scope-boot",
      "demo",
      "demo-boot-witness",
    ].toSorted();
    assert.deepEqual(lanes, expectedLanes);
    assert.deepEqual(workflow.jobs.required.needs.toSorted(), expectedLanes);
    assert.equal(workflow.defaults.run["working-directory"], "products/cache");
    const source = await readFile(
      resolve(root, ".github/workflows/cache.yml"),
      "utf-8"
    );
    const planted = parse(
      `${source}\n  planted-ungated-lane:\n    runs-on: ubuntu-latest\n`
    );
    assert.ok(Object.hasOwn(planted.jobs, "planted-ungated-lane"));
  });

  it("keeps imported workflows inert and verification free of publishers", async () => {
    const files = await readdir(resolve(root, ".github/workflows"));
    await Promise.all(
      files.map(async (file) => {
        const source = await readFile(
          resolve(root, ".github/workflows", file),
          "utf-8"
        );
        if (file === "release-env.yml") {
          const workflow = parse(source);
          assert.equal(
            workflow.jobs["release-identity"].if,
            "github.repository == 'astilbahq/astilba' && vars.ENV_PUBLISHER_ENABLED == 'true' && github.ref_type == 'tag'"
          );
          assert.deepEqual(workflow.on, { push: { tags: ["env/v*"] } });
          assert.doesNotMatch(source, /wrangler deploy|secrets\./u);
          return;
        }
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
