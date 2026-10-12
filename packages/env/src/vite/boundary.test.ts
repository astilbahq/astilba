/* oxlint-disable typescript/no-unsafe-type-assertion, typescript/promise-function-async -- These boundary tests assert complete exact matrices, invoke a Vite hook through a minimal structural context, and use deterministic promise-returning resolvers without artificial async scheduling. */

import {
  mkdirSync,
  mkdtempSync,
  realpathSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";

import {
  createBrowserBoundaryPlugin,
  locatePackageRoots,
  rawBrowserImportIsPrivate,
  resolvedPackageImportIsPrivate,
} from "./boundary.ts";
import type { PackageRoots } from "./boundary.ts";

let fixtureRoot = "";
let roots: PackageRoots;
let browserFile = "";
let privateFile = "";
let outsideAlias = "";

describe("Vite browser boundary", () => {
  beforeAll(() => {
    fixtureRoot = mkdtempSync(path.join(tmpdir(), "astilba-env-vite-"));
    const packageRoot = path.join(fixtureRoot, "package");
    const browserRoot = path.join(packageRoot, "dist", "browser");
    browserFile = path.join(browserRoot, "index.js");
    privateFile = path.join(packageRoot, "dist", "runtime", "index.js");
    outsideAlias = path.join(fixtureRoot, "runtime-alias.js");
    mkdirSync(browserRoot, { recursive: true });
    mkdirSync(path.join(packageRoot, "dist", "runtime"), {
      recursive: true,
    });
    writeFileSync(browserFile, "export {};\n");
    writeFileSync(privateFile, "export {};\n");
    symlinkSync(privateFile, outsideAlias);
    roots = Object.freeze({
      browser: realpathSync(browserRoot),
      package: realpathSync(packageRoot),
    });
  });

  afterAll(() => {
    rmSync(fixtureRoot, { force: true, recursive: true });
  });

  it("classifies exact raw package and generated paths", () => {
    expect(rawBrowserImportIsPrivate("@astilba/env")).toBe(true);
    expect(rawBrowserImportIsPrivate("@astilba/env/runtime")).toBe(true);
    expect(rawBrowserImportIsPrivate("@astilba/env/vite")).toBe(true);
    expect(rawBrowserImportIsPrivate("@astilba/env/browser")).toBe(false);
    expect(rawBrowserImportIsPrivate("@astilba/env/browser?worker")).toBe(
      false
    );
    expect(rawBrowserImportIsPrivate("./service.server.ts")).toBe(true);
    expect(rawBrowserImportIsPrivate("./astilba.env.mts")).toBe(true);
    expect(
      rawBrowserImportIsPrivate("./.astilba/env/browser/web.deployment.ts")
    ).toBe(false);
    expect(
      rawBrowserImportIsPrivate("./.ASTILBA/ENV/BROWSER/web.DEPLOYMENT.TS")
    ).toBe(false);
    expect(
      rawBrowserImportIsPrivate("./.astilba/env/browser/Web.deployment.ts")
    ).toBe(true);
    expect(
      rawBrowserImportIsPrivate(
        "./.astilba/env/consumers/web.public.json#asset"
      )
    ).toBe(false);
    expect(
      rawBrowserImportIsPrivate("./.astilba/env/contract.json?asset")
    ).toBe(true);
  });

  it("uses real containment to refuse package-private aliases", () => {
    expect(resolvedPackageImportIsPrivate(browserFile, roots)).toBe(false);
    expect(resolvedPackageImportIsPrivate(privateFile, roots)).toBe(true);
    expect(resolvedPackageImportIsPrivate(outsideAlias, roots)).toBe(true);
    expect(
      resolvedPackageImportIsPrivate(
        path.join(roots.package, "missing.js"),
        roots
      )
    ).toBe(true);
  });

  it("does not hide a missing browser root after finding this package", () => {
    const packageRoot = path.join(fixtureRoot, "incomplete-package");
    const modulePath = path.join(packageRoot, "dist", "vite", "index.js");
    mkdirSync(path.dirname(modulePath), { recursive: true });
    writeFileSync(
      path.join(packageRoot, "package.json"),
      JSON.stringify({ name: "@astilba/env" })
    );
    writeFileSync(modulePath, "export {};\n");

    expect(() => locatePackageRoots(pathToFileURL(modulePath).href)).toThrow(
      /ENOENT/u
    );
  });

  it("reports only the stable refusal and skips resolution", async () => {
    const plugin = createBrowserBoundaryPlugin(roots);
    const resolve = vi.fn<() => void>();
    const error = vi.fn<(message: string) => never>((message): never => {
      throw new Error(message);
    });
    const hook = plugin.resolveId as unknown as (
      this: { error: typeof error; resolve: typeof resolve },
      source: string,
      importer: string | undefined,
      options: { ssr: boolean }
    ) => Promise<null>;

    await expect(
      hook.call({ error, resolve }, "@astilba/env/runtime", undefined, {
        ssr: false,
      })
    ).rejects.toThrow("ENV_BROWSER_PRIVATE_IMPORT");
    expect(error).toHaveBeenCalledWith("ENV_BROWSER_PRIVATE_IMPORT");
    expect(resolve).not.toHaveBeenCalled();
  });

  it("classifies the resolved ID and leaves accepted resolution alone", async () => {
    const plugin = createBrowserBoundaryPlugin(roots);
    const error = vi.fn<(message: string) => never>((message): never => {
      throw new Error(message);
    });
    const privateResolve = vi.fn<() => Promise<{ id: string }>>(() =>
      Promise.resolve({ id: outsideAlias })
    );
    const hook = plugin.resolveId as unknown as (
      this: {
        error: typeof error;
        resolve: typeof privateResolve;
      },
      source: string,
      importer: string | undefined,
      options: { ssr: boolean }
    ) => Promise<null>;

    await expect(
      hook.call(
        { error, resolve: privateResolve },
        "runtime-alias",
        "/application.ts",
        {
          ssr: false,
        }
      )
    ).rejects.toThrow("ENV_BROWSER_PRIVATE_IMPORT");
    expect(privateResolve).toHaveBeenCalledWith(
      "runtime-alias",
      "/application.ts",
      {
        skipSelf: true,
      }
    );

    const browserResolve = vi.fn<() => Promise<{ id: string }>>(() =>
      Promise.resolve({ id: browserFile })
    );
    await expect(
      hook.call(
        { error, resolve: browserResolve },
        "@astilba/env/browser",
        "/application.ts",
        {
          ssr: false,
        }
      )
    ).resolves.toBeNull();
  });

  it("does not classify server-side resolution", async () => {
    const plugin = createBrowserBoundaryPlugin(roots);
    const resolve = vi.fn<() => void>();
    const error = vi.fn<(message: string) => never>((message): never => {
      throw new Error(message);
    });
    const hook = plugin.resolveId as unknown as (
      this: { error: typeof error; resolve: typeof resolve },
      source: string,
      importer: string | undefined,
      options: { ssr: boolean }
    ) => Promise<null>;

    await expect(
      hook.call({ error, resolve }, "@astilba/env/runtime", undefined, {
        ssr: true,
      })
    ).resolves.toBeNull();
    expect(error).not.toHaveBeenCalled();
    expect(resolve).not.toHaveBeenCalled();
  });
});
