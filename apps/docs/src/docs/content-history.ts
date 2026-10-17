import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";

import importedProvenance from "./content-provenance.json" with { type: "json" };

export type GitRunner = (arguments_: readonly string[], cwd: string) => string;

interface ImportedPage {
  contentSha256: string;
  lastModified: string;
  sourceCommit: string;
}

export interface ContentProvenance {
  importCommit: string;
  pages: Record<string, ImportedPage>;
  repository: string;
  sourceRevision: string;
  version: number;
}

interface HistoryOptions {
  cwd?: string;
  provenance?: ContentProvenance;
  runGit?: GitRunner;
}

const COMMIT = /^[a-f\d]{40}$/u;
const RAW_CHANGE =
  /^\n?:\d{6} \d{6} (?<before>[a-f\d]{40}) (?<after>[a-f\d]{40}) (?<status>[ACMDRT]\d*)$/u;
const SOURCE_PATH = /^src\/content\/docs\/(?:[^/]+\/)*[^/]+\.mdx?$/u;

export const runContentGit: GitRunner = (arguments_, cwd) =>
  execFileSync("git", arguments_, {
    cwd,
    encoding: "utf8",
    maxBuffer: 8 * 1024 * 1024,
    stdio: ["ignore", "pipe", "ignore"],
  });

export const assertDocsContentPath = (path: string): void => {
  if (
    !SOURCE_PATH.test(path) ||
    path.includes("\\") ||
    path.includes("\0") ||
    path.split("/").some((segment) => segment === "." || segment === "..")
  ) {
    throw new Error(
      `Invalid documentation source path: ${JSON.stringify(path)}.`
    );
  }
};

const normalizeDate = (date: string): string => {
  if (
    !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{3})?(?:Z|[+-]\d{2}:\d{2})$/u.test(
      date
    ) ||
    !Number.isFinite(Date.parse(date))
  ) {
    throw new Error("Sitemap source has an invalid Git date.");
  }
  return new Date(date).toISOString();
};

export const readContentHistory = (
  path: string,
  revision: string,
  cwd: string,
  git: GitRunner = runContentGit
) => {
  const fields = git(
    [
      "log",
      "--first-parent",
      "--follow",
      "--find-renames=50%",
      "--diff-merges=first-parent",
      "--format=%H%x00%cI",
      "--raw",
      "--no-abbrev",
      "-z",
      revision,
      "--",
      path,
    ],
    cwd
  ).split("\0");
  const history: {
    commit: string;
    date: string;
    path: string;
    before: string;
    after: string;
  }[] = [];
  let index = 0;
  const take = () => {
    const value = fields[index];
    index += 1;
    return value;
  };
  while (index < fields.length - 1) {
    const commit = take().trim();
    if (!COMMIT.test(commit)) {
      throw new Error("Invalid documentation Git history record.");
    }
    const date = normalizeDate(take());
    const change = RAW_CHANGE.exec(take())?.groups;
    if (!change) {
      throw new Error("Missing documentation Git change record.");
    }
    let historicalPath = take();
    const copied = change.status.startsWith("C");
    if (change.status.startsWith("R") || copied) {
      historicalPath = take();
    }
    history.push({
      commit,
      date,
      path: historicalPath,
      before: copied ? "0".repeat(40) : change.before,
      after: change.after,
    });
    // An addition (including a copy or a recreated path) starts a new page's
    // history; older occupants of the path must not supply its legacy identity.
    if (copied || change.status === "A") {
      break;
    }
  }
  return history;
};

export const verifyContentProvenance = ({
  cwd = process.cwd(),
  provenance = importedProvenance,
  runGit: git = runContentGit,
}: HistoryOptions = {}): void => {
  if (git(["rev-parse", "--is-shallow-repository"], cwd).trim() !== "false") {
    throw new Error(
      "Accurate sitemap dates require a complete Git history. Fetch the full history before building."
    );
  }
  if (
    provenance.version !== 1 ||
    provenance.repository !== "astilbahq/docs" ||
    !COMMIT.test(provenance.importCommit) ||
    !COMMIT.test(provenance.sourceRevision)
  ) {
    throw new Error("Invalid public documentation provenance identity.");
  }
  git(["merge-base", "--is-ancestor", provenance.importCommit, "HEAD"], cwd);
  const importedPaths = git(
    [
      "ls-tree",
      "--full-tree",
      "-r",
      "--name-only",
      "-z",
      provenance.importCommit,
      "--",
      "apps/docs/src/content/docs",
    ],
    cwd
  )
    .split("\0")
    .filter((path) => /\.mdx?$/u.test(path))
    .map((path) => path.replace(/^apps\/docs\//u, ""));
  if (
    JSON.stringify(importedPaths.toSorted()) !==
    JSON.stringify(Object.keys(provenance.pages).toSorted())
  ) {
    throw new Error(
      "Documentation provenance must cover every imported Markdown page exactly once."
    );
  }
  for (const [path, page] of Object.entries(provenance.pages)) {
    assertDocsContentPath(path);
    if (
      !COMMIT.test(page.sourceCommit) ||
      normalizeDate(page.lastModified) !== page.lastModified
    ) {
      throw new Error(`Invalid original content identity for ${path}.`);
    }
    const original = git(
      ["show", `${provenance.importCommit}:apps/docs/${path}`],
      cwd
    );
    if (
      createHash("sha256").update(original).digest("hex") !== page.contentSha256
    ) {
      throw new Error(`Imported content fingerprint mismatch: ${path}.`);
    }
  }
};

const findImportedDate = (
  history: ReturnType<typeof readContentHistory>,
  provenance: ContentProvenance,
  sourcePath: string
) => {
  const imported = history.find(
    ({ commit }) => commit === provenance.importCommit
  );
  if (!imported) {
    return undefined;
  }
  const path = imported.path.replace(/^apps\/docs\//u, "");
  const page = provenance.pages[path];
  if (!page) {
    throw new Error(
      `Missing imported documentation provenance: ${sourcePath}.`
    );
  }
  return page.lastModified;
};

export const getDocsContentMetadata = (
  sourcePath: string,
  {
    cwd = process.cwd(),
    provenance = importedProvenance,
    runGit: git = runContentGit,
  }: HistoryOptions = {}
): { lastModified: string } => {
  assertDocsContentPath(sourcePath);
  const history = readContentHistory(sourcePath, "HEAD", cwd, git);
  const contentChange = history.find(({ before, after }) => before !== after);
  if (!contentChange) {
    throw new Error(
      `Sitemap source has no committed history: ${JSON.stringify(sourcePath)}.`
    );
  }
  if (git(["hash-object", "--", sourcePath], cwd).trim() !== history[0].after) {
    throw new Error(
      `Commit documentation content before building production: ${sourcePath}.`
    );
  }
  const originalDate = findImportedDate(history, provenance, sourcePath);
  return {
    lastModified:
      originalDate && contentChange.commit === provenance.importCommit
        ? originalDate
        : contentChange.date,
  };
};
