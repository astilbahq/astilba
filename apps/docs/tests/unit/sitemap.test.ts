import { existsSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it, vi } from "vitest";

import {
  addDocsSitemapLastModified,
  createDocsSitemapLastModified,
  docsSitemapSources,
  type GitRunner,
} from "../../src/docs/sitemap";

const COMPLETE_HISTORY_DATE = "2026-07-14T12:34:56+01:00";

const createGitRunner = (date = COMPLETE_HISTORY_DATE): GitRunner =>
  vi.fn((arguments_) => (arguments_[0] === "rev-parse" ? "false" : date));

describe("documentation sitemap", () => {
  it("maps every public URL to one existing source file", () => {
    const canonicalPaths = docsSitemapSources.map(
      ({ canonicalPath }) => canonicalPath
    );
    const sourcePaths = docsSitemapSources.map(({ sourcePath }) => sourcePath);

    expect(docsSitemapSources).toHaveLength(36);
    expect(new Set(canonicalPaths).size).toBe(canonicalPaths.length);
    expect(new Set(sourcePaths).size).toBe(sourcePaths.length);
    expect(sourcePaths).toEqual(
      expect.arrayContaining([
        "src/content/docs/env.md",
        "src/content/docs/env/overview.md",
        "src/content/docs/env/quickstart.md",
        "src/content/docs/env/nodejs.md",
        "src/content/docs/env/browser-runtime.md",
        "src/content/docs/env/cloudflare-workers.md",
        "src/content/docs/env/vite.md",
        "src/content/docs/env/nextjs.md",
        "src/content/docs/env/lifecycles-and-projections.md",
        "src/content/docs/env/validation-and-standard-schema.md",
        "src/content/docs/env/browser-delivery.md",
        "src/content/docs/env/inventory-and-drift.md",
        "src/content/docs/env/migrate-from-next-dynamic-env.md",
        "src/content/docs/env/declaration-reference.md",
        "src/content/docs/env/cli-reference.md",
        "src/content/docs/env/release-and-support.md",
      ])
    );

    for (const sourcePath of sourcePaths) {
      expect(existsSync(resolve(process.cwd(), sourcePath))).toBe(true);
    }
  });

  it("normalizes each source commit date to UTC", () => {
    const runGit = createGitRunner();
    const lastModified = createDocsSitemapLastModified({ runGit });

    expect(lastModified.size).toBe(docsSitemapSources.length);
    expect(lastModified.get("/docs/cache/overview/")).toBe(
      "2026-07-14T11:34:56.000Z"
    );
    expect(runGit).toHaveBeenCalledWith(
      [
        "log",
        "-1",
        "--follow",
        "--format=%cI",
        "--",
        "src/content/docs/cache/overview.md",
      ],
      process.cwd()
    );
  });

  it("rejects a shallow repository", () => {
    expect(() =>
      createDocsSitemapLastModified({
        runGit: () => "true",
      })
    ).toThrow("Accurate sitemap dates require a complete Git history");
  });

  it("rejects missing or invalid source history", () => {
    expect(() =>
      createDocsSitemapLastModified({ runGit: createGitRunner("") })
    ).toThrow("Sitemap source has no committed history");
    expect(() =>
      createDocsSitemapLastModified({
        runGit: createGitRunner("not-a-date"),
      })
    ).toThrow("Sitemap source has an invalid Git date");
  });

  it("adds only mapped dates to generated sitemap entries", () => {
    const lastModified = new Map([
      ["/docs/cache/overview/", "2026-07-14T11:34:56.000Z"],
    ]);

    expect(
      addDocsSitemapLastModified(
        { priority: 0.5, url: "https://astilba.com/docs/cache/overview/" },
        lastModified
      )
    ).toEqual({
      lastmod: "2026-07-14T11:34:56.000Z",
      priority: 0.5,
      url: "https://astilba.com/docs/cache/overview/",
    });
    expect(() =>
      addDocsSitemapLastModified(
        { url: "https://astilba.com/docs/not-public/" },
        lastModified
      )
    ).toThrow("Sitemap URL has no public source mapping");
  });
});
