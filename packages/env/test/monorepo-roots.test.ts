import { execFileSync } from "node:child_process";
import {
  mkdir,
  mkdtemp,
  realpath,
  rm,
  symlink,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

type CaptureSource = (
  root: string
) => Promise<{ package: { name: string; version: string } }>;
type AssertToolContainment = (
  root: string,
  target: string
) => Promise<{ canonicalTarget: string }>;
const isCaptureSource = (value: unknown): value is CaptureSource =>
  typeof value === "function";
const isAssertToolContainment = (
  value: unknown
): value is AssertToolContainment => typeof value === "function";
const artifactModule: unknown =
  // @ts-expect-error -- The verifier is a checked JSDoc module without declarations.
  await import("../scripts/artifact-manifest.mjs");
const containmentModule: unknown =
  // @ts-expect-error -- The verifier is a checked JSDoc module without declarations.
  await import("../scripts/tool-containment.mjs");
if (
  typeof artifactModule !== "object" ||
  artifactModule === null ||
  !("captureSource" in artifactModule) ||
  !isCaptureSource(artifactModule.captureSource) ||
  typeof containmentModule !== "object" ||
  containmentModule === null ||
  !("assertToolContainment" in containmentModule) ||
  !isAssertToolContainment(containmentModule.assertToolContainment)
) {
  throw new TypeError("Migration verifiers are unavailable.");
}
const { captureSource } = artifactModule;
const { assertToolContainment } = containmentModule;

describe("monorepo roots", () => {
  it("binds nested Env metadata and rejects uncommitted package metadata", async () => {
    const root = await mkdtemp(resolve(tmpdir(), "env-source-root-"));
    const nested = resolve(root, "packages/env");
    const git = (...arguments_: string[]) =>
      execFileSync("git", ["-C", root, ...arguments_], { stdio: "pipe" });
    try {
      await mkdir(nested, { recursive: true });
      await writeFile(
        resolve(root, "package.json"),
        '{"name":"pilot","private":true}\n'
      );
      await writeFile(
        resolve(nested, "package.json"),
        '{"name":"@astilba/env","version":"0.3.0"}\n'
      );
      git("init");
      git("add", ".");
      git(
        "-c",
        "user.name=Fixture",
        "-c",
        "user.email=fixture@example.invalid",
        "commit",
        "-m",
        "fixture"
      );
      expect((await captureSource(nested)).package).toStrictEqual({
        name: "@astilba/env",
        version: "0.3.0",
      });
      await writeFile(
        resolve(nested, "package.json"),
        '{"name":"@astilba/env","version":"9.0.0"}\n'
      );
      await expect(captureSource(nested)).rejects.toThrow(
        "clean committed checkout"
      );
      await rm(resolve(nested, "package.json"));
      await writeFile(
        resolve(root, "shared-package.json"),
        '{"name":"@astilba/env","version":"0.3.0"}\n'
      );
      await symlink(
        "../../shared-package.json",
        resolve(nested, "package.json")
      );
      git("add", ".");
      git(
        "-c",
        "user.name=Fixture",
        "-c",
        "user.email=fixture@example.invalid",
        "commit",
        "-m",
        "symlink fixture"
      );
      await expect(captureSource(nested)).rejects.toThrow(
        "not the committed source"
      );
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });

  it("admits shared pnpm store tools and rejects an external symlink", async () => {
    const root = await mkdtemp(resolve(tmpdir(), "env-install-root-"));
    const nested = resolve(root, "packages/env");
    const store = resolve(
      root,
      "node_modules/.pnpm/typescript/node_modules/typescript"
    );
    const external = await mkdtemp(resolve(tmpdir(), "env-external-tool-"));
    try {
      await mkdir(resolve(nested, "node_modules"), { recursive: true });
      await mkdir(store, { recursive: true });
      await writeFile(
        resolve(root, "pnpm-workspace.yaml"),
        "packages:\n  - packages/env\n"
      );
      await writeFile(resolve(store, "package.json"), "{}\n");
      await writeFile(resolve(external, "package.json"), "{}\n");
      const kind = process.platform === "win32" ? "junction" : "dir";
      await symlink(store, resolve(nested, "node_modules/typescript"), kind);
      await symlink(external, resolve(nested, "node_modules/external"), kind);
      await expect(
        assertToolContainment(
          nested,
          resolve(nested, "node_modules/typescript/package.json")
        )
      ).resolves.toHaveProperty(
        "canonicalTarget",
        await realpath(resolve(store, "package.json"))
      );
      await expect(
        assertToolContainment(
          nested,
          resolve(nested, "node_modules/external/package.json")
        )
      ).rejects.toThrow("strict descendant");
    } finally {
      await rm(root, { recursive: true, force: true });
      await rm(external, { recursive: true, force: true });
    }
  });
});
