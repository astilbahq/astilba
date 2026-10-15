import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  mkdirSync,
  mkdtempSync,
  renameSync,
  rmSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { afterEach, describe, expect, it } from "vitest";

import {
  type ContentProvenance,
  getDocsContentMetadata,
  getDocsOriginalSource,
  readContentHistory,
  verifyContentProvenance,
} from "../../src/docs/content-history";
import provenance from "../../src/docs/content-provenance.json";
import { getDocsOriginalSourceUrl } from "../../src/docs/source";

const ORIGINAL_DATE = "2026-08-11T11:08:32.000Z";
const IMPORT_DATE = "2026-10-12T17:30:17.000Z";
const EDIT_DATE = "2026-10-15T12:00:00.000Z";
const REVERT_DATE = "2026-10-16T12:00:00.000Z";
const PAGE = "src/content/docs/env/example.md";
const REMOVED = "src/content/docs/env/removed.md";
const ORIGINAL = `${Array.from({ length: 30 }, (_, index) => `Original public paragraph ${index}.`).join("\n")}\n`;
const directories: string[] = [];

afterEach(() => {
  for (const directory of directories.splice(0)) {
    rmSync(directory, { recursive: true, force: true });
  }
});

const fixture = () => {
  const root = mkdtempSync(join(tmpdir(), "astilba-content-history-"));
  directories.push(root);
  const cwd = join(root, "apps/docs");
  const hooks = join(root, "empty-hooks");
  mkdirSync(join(cwd, "src/content/docs/env"), { recursive: true });
  mkdirSync(hooks);
  const git = (args: readonly string[], date = IMPORT_DATE) =>
    execFileSync(
      "git",
      [
        "-c",
        "user.name=Content history test",
        "-c",
        "user.email=history@example.invalid",
        "-c",
        "commit.gpgsign=false",
        "-c",
        `core.hooksPath=${hooks}`,
        "-c",
        "core.autocrlf=false",
        ...args,
      ],
      {
        cwd: root,
        encoding: "utf8",
        stdio: ["ignore", "pipe", "pipe"],
        env: {
          ...process.env,
          GIT_AUTHOR_DATE: date,
          GIT_COMMITTER_DATE: date,
        },
      }
    );
  const commit = (date = IMPORT_DATE) => {
    git(["add", "--all"]);
    git(["commit", "--allow-empty", "-m", "Public content fixture"], date);
    return git(["rev-parse", "HEAD"]).trim();
  };
  git(["init", "--initial-branch=main"]);
  writeFileSync(join(cwd, PAGE), ORIGINAL);
  writeFileSync(join(cwd, REMOVED), "Retired public page.\n");
  const importCommit = commit();
  const imported: ContentProvenance = {
    version: 1,
    repository: "astilbahq/docs",
    sourceRevision: "1".repeat(40),
    importCommit,
    pages: Object.fromEntries(
      [
        [PAGE, ORIGINAL],
        [REMOVED, "Retired public page.\n"],
      ].map(([path, bytes]) => [
        path,
        {
          contentSha256: createHash("sha256").update(bytes).digest("hex"),
          lastModified: ORIGINAL_DATE,
          sourceCommit: "2".repeat(40),
        },
      ])
    ),
  };
  const metadata = (path = PAGE) =>
    getDocsContentMetadata(path, { cwd, provenance: imported });
  return { root, cwd, git, commit, imported, metadata };
};

describe("public Docs content provenance", () => {
  it("checks every pinned public page against its immutable monorepo import bytes", () => {
    expect(provenance.sourceRevision).toBe(
      "69101c58b1494689a3ec48ebd3e059f5d33ce729"
    );
    expect(provenance.importCommit).toBe(
      "a257d04d3e32db5025bac2f8aaa1ed0d0470f09a"
    );
    expect(Object.keys(provenance.pages)).toHaveLength(36);
    verifyContentProvenance();
    expect(provenance.pages["src/content/docs/env/overview.md"]).toMatchObject({
      lastModified: ORIGINAL_DATE,
      sourceCommit: "0a14681acaa76ca98d30bb182330d4603bb256e4",
    });
    expect(getDocsOriginalSourceUrl(undefined, "env/overview")).toBe(
      "https://github.com/astilbahq/docs/blob/0a14681acaa76ca98d30bb182330d4603bb256e4/src/content/docs/env/overview.md"
    );
  });

  it("retains original dates across imports and unrelated monorepo commits", () => {
    const { cwd, imported, commit, metadata } = fixture();
    verifyContentProvenance({ cwd, provenance: imported });
    commit(EDIT_DATE);
    expect(metadata().lastModified).toBe(ORIGINAL_DATE);
    expect(metadata().originalSource).toContain(
      `/blob/${"2".repeat(40)}/${PAGE}`
    );
  });

  it("advances dates for edits and subsequent reverts without losing attribution", () => {
    const { cwd, commit, metadata } = fixture();
    writeFileSync(join(cwd, PAGE), `${ORIGINAL}A later content edit.\n`);
    commit(EDIT_DATE);
    expect(metadata().lastModified).toBe(EDIT_DATE);
    expect(metadata().originalSource).toContain(
      `/blob/${"2".repeat(40)}/${PAGE}`
    );
    writeFileSync(join(cwd, PAGE), ORIGINAL);
    commit(REVERT_DATE);
    expect(metadata().lastModified).toBe(REVERT_DATE);
  });

  it("follows pure and edited renames while preserving the original source path", () => {
    const { cwd, commit, metadata } = fixture();
    const renamed = "src/content/docs/env/renamed.md";
    renameSync(join(cwd, PAGE), join(cwd, renamed));
    commit(EDIT_DATE);
    expect(metadata(renamed).lastModified).toBe(ORIGINAL_DATE);
    expect(metadata(renamed).originalSource).toContain(
      `/blob/${"2".repeat(40)}/${PAGE}`
    );
    renameSync(join(cwd, renamed), join(cwd, PAGE));
    writeFileSync(
      join(cwd, PAGE),
      `${ORIGINAL}An edit accompanying the rename.\n`
    );
    commit(REVERT_DATE);
    expect(metadata().lastModified).toBe(REVERT_DATE);
    expect(metadata().originalSource).toContain(
      `/blob/${"2".repeat(40)}/${PAGE}`
    );
  });

  it("dates new and copied pages from their own creation without fabricating legacy ancestry", () => {
    const { cwd, commit, metadata } = fixture();
    const added = "src/content/docs/env/new.md";
    writeFileSync(join(cwd, added), ORIGINAL);
    const distinct = "src/content/docs/env/distinct.md";
    writeFileSync(join(cwd, distinct), "A newly authored public page.\n");
    commit(EDIT_DATE);
    expect(metadata(added)).toEqual({ lastModified: EDIT_DATE });
    expect(metadata(distinct)).toEqual({ lastModified: EDIT_DATE });
  });

  it("does not advance content dates for mode-only changes", () => {
    const { git, metadata } = fixture();
    git(["update-index", "--chmod=+x", `apps/docs/${PAGE}`]);
    git(["commit", "-m", "Mode-only fixture"], EDIT_DATE);
    expect(metadata().lastModified).toBe(ORIGINAL_DATE);
  });

  it("retains deleted-page provenance but cannot emit metadata for a deleted source", () => {
    const { cwd, commit, imported, metadata } = fixture();
    unlinkSync(join(cwd, REMOVED));
    commit(EDIT_DATE);
    verifyContentProvenance({ cwd, provenance: imported });
    expect(metadata().lastModified).toBe(ORIGINAL_DATE);
    expect(() => metadata(REMOVED)).toThrow();
    expect(imported.pages[REMOVED]).toBeDefined();
    writeFileSync(join(cwd, REMOVED), "Retired public page.\n");
    commit(REVERT_DATE);
    expect(metadata(REMOVED)).toEqual({ lastModified: REVERT_DATE });
  });

  it("rejects uncommitted page content and missing source history", () => {
    const { cwd, imported, metadata } = fixture();
    writeFileSync(join(cwd, PAGE), "Not committed.\n");
    expect(() => metadata()).toThrow(
      "Commit documentation content before building production"
    );
    expect(() => metadata("src/content/docs/env/missing.md")).toThrow(
      "Sitemap source has no committed history"
    );
    expect(
      getDocsOriginalSource(PAGE, { cwd, provenance: imported })
    ).toContain(`/blob/${"2".repeat(40)}/${PAGE}`);
    expect(
      getDocsOriginalSource("src/content/docs/env/missing.md", {
        cwd,
        provenance: imported,
      })
    ).toBeUndefined();
  });

  it("rejects missing import coverage, altered fingerprints and invalid identities", () => {
    const { cwd, imported } = fixture();
    const altered = structuredClone(imported);
    altered.pages[PAGE].contentSha256 = "0".repeat(64);
    expect(() => verifyContentProvenance({ cwd, provenance: altered })).toThrow(
      "Imported content fingerprint mismatch"
    );
    altered.pages = Object.fromEntries(
      Object.entries(altered.pages).filter(([path]) => path !== PAGE)
    );
    expect(() => verifyContentProvenance({ cwd, provenance: altered })).toThrow(
      "every imported Markdown page"
    );
    expect(() =>
      verifyContentProvenance({
        cwd,
        provenance: { ...imported, repository: "private/repository" },
      })
    ).toThrow("Invalid public documentation provenance identity");
  });

  it("fails closed on malformed raw Git history", () => {
    expect(() =>
      readContentHistory(
        PAGE,
        "HEAD",
        process.cwd(),
        () => `not-a-commit\0date\0`
      )
    ).toThrow("Invalid documentation Git history record");
    expect(() =>
      readContentHistory(
        PAGE,
        "HEAD",
        process.cwd(),
        () => `${"1".repeat(40)}\0${EDIT_DATE}\0bad-change\0`
      )
    ).toThrow("Missing documentation Git change record");
  });
});
