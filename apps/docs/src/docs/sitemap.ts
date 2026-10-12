import { execFileSync } from "node:child_process";
import { statSync } from "node:fs";
import { isAbsolute, resolve } from "node:path";

import { docsProducts, getPageHref } from "./catalog.ts";
import { siteDocsPages } from "./site-pages.ts";

export interface DocsSitemapSource {
  canonicalPath: string;
  sourcePath: string;
}

export type GitRunner = (arguments_: readonly string[], cwd: string) => string;

interface LastModifiedOptions {
  cwd?: string;
  runGit?: GitRunner;
}

const runGit: GitRunner = (arguments_, cwd) =>
  execFileSync("git", arguments_, {
    cwd,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "ignore"],
  }).trim();

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

const getGitOutput = (
  arguments_: readonly string[],
  cwd: string,
  git: GitRunner,
  description: string
): string => {
  try {
    return git(arguments_, cwd).trim();
  } catch (error) {
    throw new Error(`Could not ${description}.`, { cause: error });
  }
};

export const createDocsSitemapLastModified = ({
  cwd = process.cwd(),
  runGit: git = runGit,
}: LastModifiedOptions = {}): ReadonlyMap<string, string> => {
  assertSitemapSources(cwd);

  const shallow = getGitOutput(
    ["rev-parse", "--is-shallow-repository"],
    cwd,
    git,
    "inspect the Git history used for sitemap dates"
  );

  if (shallow !== "false") {
    throw new Error(
      "Accurate sitemap dates require a complete Git history. Fetch the full history before building."
    );
  }

  return new Map(
    docsSitemapSources.map(({ canonicalPath, sourcePath }) => {
      const rawDate = getGitOutput(
        ["log", "-1", "--follow", "--format=%cI", "--", sourcePath],
        cwd,
        git,
        `read the last modification for ${JSON.stringify(sourcePath)}`
      );

      if (!rawDate) {
        throw new Error(
          `Sitemap source has no committed history: ${JSON.stringify(sourcePath)}.`
        );
      }

      const timestamp = Date.parse(rawDate);

      if (!Number.isFinite(timestamp)) {
        throw new TypeError(
          `Sitemap source has an invalid Git date: ${JSON.stringify(sourcePath)}.`
        );
      }

      return [canonicalPath, new Date(timestamp).toISOString()] as const;
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
