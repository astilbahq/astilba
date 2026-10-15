import { statSync } from "node:fs";
import { isAbsolute, resolve } from "node:path";

import { docsProducts, getPageHref } from "./catalog.ts";
import {
  getDocsContentMetadata,
  type GitRunner,
  runContentGit,
  verifyContentProvenance,
} from "./content-history.ts";
import { siteDocsPages } from "./site-pages.ts";

export type { GitRunner } from "./content-history.ts";

export interface DocsSitemapSource {
  canonicalPath: string;
  sourcePath: string;
}

interface LastModifiedOptions {
  cwd?: string;
  runGit?: GitRunner;
}

export const docsSitemapSources = Object.freeze([
  ...siteDocsPages.map(({ canonicalPath, sourcePath }) => ({
    canonicalPath,
    sourcePath,
  })),
  ...docsProducts.flatMap((product) =>
    product.versions.flatMap((version) =>
      version.sections.flatMap((section) =>
        section.items.map((page) => ({
          canonicalPath: getPageHref(version, page),
          sourcePath: page.sourcePath,
        }))
      )
    )
  ),
] satisfies DocsSitemapSource[]);

const assertSitemapSources = (cwd: string): void => {
  const canonicalPaths = new Set<string>();
  const sourcePaths = new Set<string>();

  for (const { canonicalPath, sourcePath } of docsSitemapSources) {
    if (canonicalPaths.has(canonicalPath)) {
      throw new Error(
        `Duplicate sitemap path: ${JSON.stringify(canonicalPath)}.`
      );
    }

    if (sourcePaths.has(sourcePath)) {
      throw new Error(
        `Duplicate sitemap source: ${JSON.stringify(sourcePath)}.`
      );
    }

    const pathSegments = sourcePath.replaceAll("\\", "/").split("/");

    if (isAbsolute(sourcePath) || pathSegments.includes("..")) {
      throw new Error(
        `Sitemap source must be repository-relative: ${JSON.stringify(sourcePath)}.`
      );
    }

    let sourceStats;

    try {
      sourceStats = statSync(resolve(cwd, sourcePath));
    } catch (error) {
      throw new Error(
        `Missing sitemap source: ${JSON.stringify(sourcePath)}.`,
        { cause: error }
      );
    }

    if (!sourceStats.isFile()) {
      throw new Error(
        `Sitemap source is not a file: ${JSON.stringify(sourcePath)}.`
      );
    }

    canonicalPaths.add(canonicalPath);
    sourcePaths.add(sourcePath);
  }
};

export const createDocsSitemapLastModified = ({
  cwd = process.cwd(),
  runGit: git = runContentGit,
}: LastModifiedOptions = {}): ReadonlyMap<string, string> => {
  assertSitemapSources(cwd);
  verifyContentProvenance({ cwd, runGit: git });

  return new Map(
    docsSitemapSources.map(({ canonicalPath, sourcePath }) => {
      return [
        canonicalPath,
        getDocsContentMetadata(sourcePath, { cwd, runGit: git }).lastModified,
      ] as const;
    })
  );
};

export const addDocsSitemapLastModified = <T extends { url: string }>(
  item: T,
  lastModifiedByPath: ReadonlyMap<string, string>
): T & { lastmod: string } => {
  const pathname = new URL(item.url).pathname;
  const lastmod = lastModifiedByPath.get(pathname);

  if (!lastmod) {
    throw new Error(`Sitemap URL has no public source mapping: ${item.url}.`);
  }

  return { ...item, lastmod };
};
