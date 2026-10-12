import {
  access,
  mkdtemp,
  mkdir,
  readFile,
  rm,
  symlink,
  truncate,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";

import { afterEach, describe, expect, it } from "vitest";

import {
  inspectGeneratedDirectory,
  replaceGeneratedDirectory,
} from "./output.ts";

const GENERATED_FORMAT_PREFIX = "astilba.env.generated/v";
const MAXIMUM_FILE_BYTES = 8_388_608;
const temporaryRoots: string[] = [];

const temporaryRoot = async (prefix: string): Promise<string> => {
  const root = await mkdtemp(join(tmpdir(), prefix));
  temporaryRoots.push(root);
  return root;
};

const canonicalManifest = (files: readonly string[], version = "1"): string =>
  `${JSON.stringify({
    files: files.toSorted(),
    format: `${GENERATED_FORMAT_PREFIX}${version}`,
  })}\n`;

const writeOwnedTree = async (
  root: string,
  files: readonly string[] = ["contract.json"],
  manifest: string = canonicalManifest(files)
): Promise<string> => {
  const target = join(root, ".astilba", "env");
  await mkdir(target, { recursive: true });
  for (const path of files) {
    const output = join(target, path);
    await mkdir(dirname(output), { recursive: true });
    await writeFile(output, "{}\n");
  }
  await writeFile(join(target, "manifest.json"), manifest);
  return target;
};

describe("generated output ownership", () => {
  afterEach(async () => {
    for (const root of temporaryRoots.splice(0)) {
      await rm(root, { force: true, recursive: true });
    }
  });

  it("replaces only a completely owned current-format directory", async () => {
    const trusted = await temporaryRoot("astilba-env-output-");
    const target = await writeOwnedTree(trusted);
    const state = await inspectGeneratedDirectory(trusted, target);

    await replaceGeneratedDirectory(trusted, target, state);

    await expect(access(target)).resolves.toBeUndefined();
    await expect(access(join(target, "contract.json"))).rejects.toMatchObject({
      code: "ENOENT",
    });
  });

  it("refuses an unowned directory without deleting its contents", async () => {
    const trusted = await temporaryRoot("astilba-env-output-");
    const target = join(trusted, ".astilba", "env");
    const sentinel = join(target, "sentinel.txt");
    await mkdir(target, { recursive: true });
    await writeFile(sentinel, "preserve");

    await expect(
      inspectGeneratedDirectory(trusted, target)
    ).rejects.toMatchObject({ code: "ENV_GENERATED_INVALID" });
    await expect(readFile(sentinel, "utf-8")).resolves.toBe("preserve");
  });

  it("refuses a recognised newer manifest before replacement", async () => {
    const trusted = await temporaryRoot("astilba-env-output-");
    const target = await writeOwnedTree(
      trusted,
      ["contract.json"],
      canonicalManifest(["contract.json"], "2")
    );

    await expect(
      inspectGeneratedDirectory(trusted, target)
    ).rejects.toMatchObject({
      code: "ENV_GENERATED_FORMAT_UNSUPPORTED",
    });
    await expect(
      readFile(join(target, "contract.json"), "utf-8")
    ).resolves.toBe("{}\n");
  });

  it.each(["10", "19"])(
    "refuses recognised future manifest version %s",
    async (version) => {
      const trusted = await temporaryRoot("astilba-env-output-");
      const target = await writeOwnedTree(
        trusted,
        ["contract.json"],
        canonicalManifest(["contract.json"], version)
      );

      await expect(
        inspectGeneratedDirectory(trusted, target)
      ).rejects.toMatchObject({
        code: "ENV_GENERATED_FORMAT_UNSUPPORTED",
      });
      await expect(
        readFile(join(target, "contract.json"), "utf-8")
      ).resolves.toBe("{}\n");
    }
  );

  it.each([
    [
      "noncanonical key order",
      '{"format":"astilba.env.generated/v1","files":["contract.json"]}\n',
    ],
    [
      "missing terminal LF",
      '{"files":["contract.json"],"format":"astilba.env.generated/v1"}',
    ],
  ])("refuses a %s manifest", async (_case, manifest) => {
    const trusted = await temporaryRoot("astilba-env-output-");
    const target = await writeOwnedTree(trusted, ["contract.json"], manifest);

    await expect(
      inspectGeneratedDirectory(trusted, target)
    ).rejects.toMatchObject({ code: "ENV_GENERATED_INVALID" });
    await expect(
      readFile(join(target, "contract.json"), "utf-8")
    ).resolves.toBe("{}\n");
  });

  it("accepts full mixed-case LocalId generated paths", async () => {
    const trusted = await temporaryRoot("astilba-env-output-");
    const files = [
      "browser/browserApp.deployment.ts",
      "consumers/browserApp.public.json",
      "serverProd.server.ts",
    ];
    const target = await writeOwnedTree(trusted, files);

    await expect(
      inspectGeneratedDirectory(trusted, target)
    ).resolves.toMatchObject({
      exists: true,
      files,
    });
  });

  it("caps the manifest at 2,047 listed files", async () => {
    const trusted = await temporaryRoot("astilba-env-output-");
    const files = Array.from(
      { length: 2048 },
      (_, index) => `a${index}.server.ts`
    ).toSorted();
    const target = await writeOwnedTree(trusted, [], canonicalManifest(files));

    await expect(
      inspectGeneratedDirectory(trusted, target)
    ).rejects.toMatchObject({ code: "ENV_GENERATED_INVALID" });
  });

  it("refuses an oversized manifest before walking the tree", async () => {
    const trusted = await temporaryRoot("astilba-env-output-");
    const target = join(trusted, ".astilba", "env");
    const manifest = join(target, "manifest.json");
    await mkdir(target, { recursive: true });
    await writeFile(manifest, "");
    await truncate(manifest, MAXIMUM_FILE_BYTES + 1);

    await expect(
      inspectGeneratedDirectory(trusted, target)
    ).rejects.toMatchObject({ code: "ENV_GENERATED_INVALID" });
  });

  it("enforces the cumulative generated tree byte bound", async () => {
    const trusted = await temporaryRoot("astilba-env-output-");
    const files = [
      ...Array.from(
        { length: 4 },
        (_, index) => `browser/a${index}.deployment.ts`
      ),
      ...Array.from(
        { length: 5 },
        (_, index) => `consumers/a${index}.public.json`
      ),
    ].toSorted();
    const target = await writeOwnedTree(trusted, files);
    for (const path of files) {
      await truncate(join(target, path), MAXIMUM_FILE_BYTES);
    }

    await expect(
      inspectGeneratedDirectory(trusted, target)
    ).rejects.toMatchObject({ code: "ENV_GENERATED_INVALID" });
  });

  it("refuses directories which are not required by the manifest", async () => {
    const trusted = await temporaryRoot("astilba-env-output-");
    const target = await writeOwnedTree(trusted);
    await mkdir(join(target, "unexpected", "nested"), { recursive: true });

    await expect(
      inspectGeneratedDirectory(trusted, target)
    ).rejects.toMatchObject({ code: "ENV_GENERATED_INVALID" });
  });

  it("refuses a symbolic-link ancestor without touching its destination", async () => {
    const trusted = await temporaryRoot("astilba-env-output-");
    const outside = await temporaryRoot("astilba-env-outside-");
    const sentinel = join(outside, "sentinel.txt");
    await writeFile(sentinel, "preserve");
    await symlink(outside, join(trusted, "redirect"));

    await expect(
      inspectGeneratedDirectory(trusted, join(trusted, "redirect", "build"))
    ).rejects.toMatchObject({ code: "ENV_GENERATED_INVALID" });
    await expect(readFile(sentinel, "utf-8")).resolves.toBe("preserve");
  });

  it("refuses a symbolic manifest instead of following it during ownership inspection", async () => {
    const trusted = await temporaryRoot("astilba-env-output-");
    const outside = await temporaryRoot("astilba-env-outside-");
    const target = await writeOwnedTree(trusted);
    const manifest = join(target, "manifest.json");
    const replacement = join(outside, "manifest.json");
    await writeFile(replacement, canonicalManifest(["contract.json"]));
    await rm(manifest);
    await symlink(replacement, manifest);

    await expect(
      inspectGeneratedDirectory(trusted, target)
    ).rejects.toMatchObject({ code: "ENV_GENERATED_INVALID" });
    await expect(readFile(replacement, "utf-8")).resolves.toBe(
      canonicalManifest(["contract.json"])
    );
  });
});
