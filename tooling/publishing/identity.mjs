import { execFileSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import nodePath from "node:path";
import { pathToFileURL } from "node:url";
import { isDeepStrictEqual } from "node:util";

const { resolve } = nodePath;
export const root = resolve(import.meta.dirname, "../..");
export const cutover = JSON.parse(
  await readFile(new URL("cutover.json", import.meta.url), "utf-8")
);
const stableVersion = /^(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)$/u;
const gitObject = /^[0-9a-f]{40}$/u;

export const releaseVersion = (product, tag) => {
  if (!Object.hasOwn(cutover.products, product)) {
    throw new Error("Only Env and coordinated tokens/UI have publisher paths.");
  }
  const prefix = cutover.products[product].tagPrefix;
  if (typeof tag !== "string" || !tag.startsWith(prefix)) {
    throw new Error("Release tag does not belong to this product namespace.");
  }
  const version = tag.slice(prefix.length);
  if (!stableVersion.test(version)) {
    throw new Error("Release tag must contain one canonical stable version.");
  }
  return version;
};

export const publisherMetadata = (directory) => ({
  bugs: { url: "https://github.com/astilbahq/astilba/issues" },
  homepage: `https://github.com/astilbahq/astilba/tree/main/${directory}#readme`,
  repository: {
    directory,
    type: "git",
    url: "git+https://github.com/astilbahq/astilba.git",
  },
});

export const verifyIdentity = ({
  product,
  tag,
  repository,
  commit,
  currentMain,
  tagCommit,
  manifests,
}) => {
  const version = releaseVersion(product, tag);
  if (
    repository !== cutover.repository ||
    !gitObject.test(commit) ||
    commit !== currentMain ||
    commit !== tagCommit
  ) {
    throw new Error(
      "Release identity must be the tagged current monorepo main."
    );
  }
  const { packages } = cutover.products[product];
  if (!Array.isArray(manifests) || manifests.length !== packages.length) {
    throw new Error("Release package set is not exact.");
  }
  for (const [index, entry] of packages.entries()) {
    const manifest = manifests[index];
    const target = publisherMetadata(entry.directory);
    if (
      manifest?.name !== entry.name ||
      manifest.version !== version ||
      manifest.private === true ||
      !isDeepStrictEqual(manifest.repository, target.repository) ||
      !isDeepStrictEqual(manifest.bugs, target.bugs) ||
      manifest.homepage !== target.homepage ||
      !isDeepStrictEqual(manifest.publishConfig, {
        access: "public",
        provenance: true,
      })
    ) {
      throw new Error(
        "Publisher metadata is not activated for the exact package set."
      );
    }
  }
  return {
    commit,
    packages: packages.map((p) => p.name),
    product,
    tag,
    version,
  };
};

export const verifyCheckoutIdentity = async (product, tag) => {
  releaseVersion(product, tag);
  const git = (...arguments_) =>
    execFileSync("git", arguments_, { cwd: root, encoding: "utf-8" }).trim();
  execFileSync(
    "git",
    ["fetch", "--no-tags", "origin", "main:refs/remotes/origin/main"],
    {
      cwd: root,
      stdio: "inherit",
    }
  );
  if (git("status", "--porcelain=v1", "--untracked-files=all")) {
    throw new Error("Release checkout must be clean.");
  }
  const manifests = await Promise.all(
    cutover.products[product].packages.map(async ({ directory }) =>
      JSON.parse(
        await readFile(resolve(root, directory, "package.json"), "utf-8")
      )
    )
  );
  return verifyIdentity({
    commit: git("rev-parse", "HEAD"),
    currentMain: git("rev-parse", "origin/main"),
    manifests,
    product,
    repository: process.env.GITHUB_REPOSITORY,
    tag,
    tagCommit: git("rev-parse", `refs/tags/${tag}^{commit}`),
  });
};

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  const [product, tag, ...extra] = process.argv.slice(2);
  if (extra.length || !product || !tag) {
    throw new Error(
      "Usage: node tooling/publishing/identity.mjs <env|ui> <namespaced-tag>"
    );
  }
  process.stdout.write(
    `${JSON.stringify(await verifyCheckoutIdentity(product, tag))}\n`
  );
}
