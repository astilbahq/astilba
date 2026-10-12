import { readFileSync, realpathSync, statSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import type { Plugin } from "vite";

const PACKAGE_NAME = "@astilba/env";
const PRIVATE_IMPORT_ERROR = "ENV_BROWSER_PRIVATE_IMPORT";
const LOCAL_ID = /^[a-z][A-Za-z0-9]{0,63}$/u;
const SERVER_MODULE = /^[^/]+\.server(?:\.d)?\.(?:[cm]?[jt]sx?|json)$/u;
const ENVIRONMENT_MODULE = /^astilba\.env(?:\.d)?\.(?:[cm]?[jt]s)$/u;
const ASCII_UPPERCASE = /[A-Z]/gu;

export type PackageRoots = Readonly<{
  browser: string;
  package: string;
}>;

const asciiFold = (value: string): string =>
  value.replace(ASCII_UPPERCASE, (character) =>
    String.fromCodePoint((character.codePointAt(0) ?? 0) + 32)
  );

const withoutSuffix = (value: string): string => {
  const query = value.indexOf("?");
  const hash = value.indexOf("#");
  const suffixStarts = [query, hash].filter((index) => index !== -1);
  const cut = suffixStarts.length === 0 ? -1 : Math.min(...suffixStarts);
  return cut === -1 ? value : value.slice(0, cut);
};

const generatedPathIsAllowed = (generatedPath: string): boolean => {
  const components = generatedPath.split("/");
  if (components.length !== 2) {
    return false;
  }
  const [directory, filename] = components;
  if (directory === undefined || filename === undefined) {
    return false;
  }
  const foldedDirectory = asciiFold(directory);
  const foldedFilename = asciiFold(filename);
  if (foldedDirectory === "browser") {
    for (const suffix of [".build.ts", ".deployment.ts", ".request.ts"]) {
      if (foldedFilename.endsWith(suffix)) {
        return LOCAL_ID.test(filename.slice(0, -suffix.length));
      }
    }
    return false;
  }
  if (
    foldedDirectory === "consumers" &&
    foldedFilename.endsWith(".public.json")
  ) {
    return LOCAL_ID.test(filename.slice(0, -".public.json".length));
  }
  return false;
};

export const rawBrowserImportIsPrivate = (rawId: string): boolean => {
  const id = withoutSuffix(rawId).replaceAll("\\", "/");
  const folded = asciiFold(id);
  if (
    (folded === PACKAGE_NAME || folded.startsWith(`${PACKAGE_NAME}/`)) &&
    folded !== `${PACKAGE_NAME}/browser`
  ) {
    return true;
  }

  const finalComponent = folded.slice(folded.lastIndexOf("/") + 1);
  if (
    SERVER_MODULE.test(finalComponent) ||
    ENVIRONMENT_MODULE.test(finalComponent)
  ) {
    return true;
  }

  const generated = /(?:^|\/)\.astilba\/env\//u.exec(folded);
  if (generated === null) {
    return false;
  }
  const remainder = id.slice(generated.index + generated[0].length);
  return !generatedPathIsAllowed(remainder);
};

const isContained = (
  root: string,
  candidate: string,
  strict: boolean
): boolean => {
  const relativePath = path.relative(root, candidate);
  if (relativePath === "") {
    return !strict;
  }
  return (
    relativePath !== ".." &&
    !relativePath.startsWith(`..${path.sep}`) &&
    !path.isAbsolute(relativePath)
  );
};

const filesystemPath = (resolvedId: string): string | undefined => {
  const id = withoutSuffix(resolvedId);
  try {
    if (id.startsWith("file:")) {
      return path.resolve(fileURLToPath(new URL(id)));
    }
    if (id.startsWith("/@fs/")) {
      return path.resolve(id.slice("/@fs/".length));
    }
    return path.isAbsolute(id) ? path.resolve(id) : undefined;
  } catch {
    return undefined;
  }
};

export const resolvedPackageImportIsPrivate = (
  resolvedId: string,
  roots: PackageRoots
): boolean => {
  const lexicalPath = filesystemPath(resolvedId);
  if (lexicalPath === undefined) {
    return false;
  }
  const lexicalIsOwned = isContained(roots.package, lexicalPath, false);

  let realPath: string;
  try {
    realPath = realpathSync(lexicalPath);
  } catch {
    return lexicalIsOwned;
  }
  const realIsOwned = isContained(roots.package, realPath, false);
  if (!lexicalIsOwned && !realIsOwned) {
    return false;
  }
  try {
    return !(
      statSync(realPath).isFile() && isContained(roots.browser, realPath, true)
    );
  } catch {
    return true;
  }
};

export const locatePackageRoots = (moduleUrl: string): PackageRoots => {
  let directory = path.dirname(fileURLToPath(moduleUrl));
  while (true) {
    const packageJson = path.join(directory, "package.json");
    let parsed: unknown;
    try {
      parsed = JSON.parse(readFileSync(packageJson, "utf-8"));
    } catch {
      // Continue to the parent until the package-owned manifest is found.
    }
    if (
      typeof parsed === "object" &&
      parsed !== null &&
      Object.hasOwn(parsed, "name") &&
      Reflect.get(parsed, "name") === PACKAGE_NAME
    ) {
      return Object.freeze({
        browser: realpathSync(path.join(directory, "dist", "browser")),
        package: realpathSync(directory),
      });
    }
    const parent = path.dirname(directory);
    if (parent === directory) {
      throw new Error("Astilba Env package root could not be located.");
    }
    directory = parent;
  }
};

const reject = (
  id: string,
  roots: PackageRoots | undefined,
  fail: (message: string) => never
): void => {
  if (
    (roots !== undefined && resolvedPackageImportIsPrivate(id, roots)) ||
    rawBrowserImportIsPrivate(id)
  ) {
    fail(PRIVATE_IMPORT_ERROR);
  }
};

export const createBrowserBoundaryPlugin = (roots: PackageRoots): Plugin => ({
  enforce: "pre",
  name: "astilba-env-browser-boundary",
  // oxlint-disable-next-line sonarjs/no-invariant-returns -- The Vite hook inspects resolution for policy violations without replacing accepted resolutions.
  async resolveId(source, importer, options) {
    if (options.ssr === true) {
      return null;
    }
    reject(source, undefined, (message) => this.error(message));
    const resolved = await this.resolve(source, importer, {
      skipSelf: true,
    });
    if (resolved !== null) {
      reject(resolved.id, roots, (message) => this.error(message));
    }
    return null;
  },
});
