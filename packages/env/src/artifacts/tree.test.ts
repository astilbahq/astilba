import { mkdtemp, mkdir, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { brotliCompressSync, gzipSync } from "node:zlib";

import { describe, expect, it } from "vitest";

import { inspectTree, scanTree } from "./tree.ts";

describe("artifact tree evidence", () => {
  it("is independent of file creation order and timestamps", async () => {
    const left = await mkdtemp(join(tmpdir(), "astilba-env-left-"));
    const right = await mkdtemp(join(tmpdir(), "astilba-env-right-"));
    await mkdir(join(left, "nested"));
    await mkdir(join(right, "nested"));
    await writeFile(join(left, "z.txt"), "z");
    await writeFile(join(left, "nested", "a.txt"), "a");
    await writeFile(join(right, "nested", "a.txt"), "a");
    await writeFile(join(right, "z.txt"), "z");

    expect((await inspectTree(left)).digest).toBe(
      (await inspectTree(right)).digest
    );
  });

  it("reports exact files and needles without writing evidence", async () => {
    const root = await mkdtemp(join(tmpdir(), "astilba-env-scan-"));
    await writeFile(join(root, "bundle.js"), "public sensitive-test-value");

    await expect(
      scanTree(root, ["sensitive-test-value", "missing"])
    ).resolves.toStrictEqual([
      { file: "bundle.js", needle: "sensitive-test-value" },
    ]);
  });

  it("can bind a selected source tree without generated files", async () => {
    const root = await mkdtemp(join(tmpdir(), "astilba-env-source-"));
    await mkdir(join(root, "generated"));
    await writeFile(join(root, "source.ts"), "source");
    await writeFile(join(root, "generated", "runtime.ts"), "generated");

    const evidence = await inspectTree(root, {
      exclude: (path, kind) => kind === "directory" && path === "generated",
    });

    expect(evidence.files.map((file) => file.path)).toStrictEqual([
      "source.ts",
    ]);
  });

  it("scans gzip and Brotli representations before declaring them clean", async () => {
    const root = await mkdtemp(join(tmpdir(), "astilba-env-compressed-"));
    await writeFile(
      join(root, "bundle.js.gz"),
      gzipSync("public private-gzip-canary")
    );
    await writeFile(
      join(root, "bundle.js.br"),
      brotliCompressSync("public private-brotli-canary")
    );

    await expect(
      scanTree(root, ["private-gzip-canary", "private-brotli-canary"])
    ).resolves.toStrictEqual([
      {
        file: "bundle.js.br",
        needle: "private-brotli-canary",
      },
      {
        file: "bundle.js.gz",
        needle: "private-gzip-canary",
      },
    ]);
  });

  it("rejects symbolic links instead of omitting them from evidence", async () => {
    const root = await mkdtemp(join(tmpdir(), "astilba-env-symlink-"));
    const outside = await mkdtemp(join(tmpdir(), "astilba-env-outside-"));
    const privateFile = join(outside, "private.txt");
    await writeFile(privateFile, "sensitive-test-value");
    await symlink(privateFile, join(root, "linked.txt"));

    await expect(inspectTree(root)).rejects.toThrow("symbolic links");
    await expect(scanTree(root, ["sensitive-test-value"])).rejects.toThrow(
      "symbolic links"
    );
  });
});
