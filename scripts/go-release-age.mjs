import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { download } from "./fetch-attempts.mjs";

// Go has no setting like pnpm's minimumReleaseAge, so this applies the same gate to
// every module version a branch's go.mod files gained since a base commit. See
// .claude/skills/upgrade-dependencies/SKILL.md.

const root = resolve(import.meta.dirname, "..");
const proxy = "https://proxy.golang.org";
const index = "https://index.golang.org/index";
const indexPage = 2000;

// The proxy spells a capital letter in a module path or version as "!" and the letter.
export const escapeModule = (text) =>
  text.replace(/[A-Z]/g, (letter) => `!${letter.toLowerCase()}`);

// The gate is pnpm-workspace.yaml's minimumReleaseAge, so the two cannot drift.
export function minimumAgeMs(workspaceText) {
  const minutes = /^minimumReleaseAge:\s*(\d+)\s*$/m.exec(workspaceText)?.[1];
  if (!minutes) throw new Error("pnpm-workspace.yaml: no minimumReleaseAge");
  return Number(minutes) * 60_000;
}

// The module versions a go.mod makes Go download, from `go mod edit -json`. A
// replacement takes the place of what it replaces, and one by a local directory
// downloads nothing.
export function downloadedVersions(modfile) {
  const versions = new Set();
  for (const { Path, Version } of modfile?.Require ?? []) {
    const replacement = (modfile.Replace ?? []).find(
      ({ Old }) =>
        Old.Path === Path && (Old.Version === undefined || Old.Version === Version),
    )?.New;
    if (!replacement) versions.add(`${Path}@${Version}`);
    else if (replacement.Version)
      versions.add(`${replacement.Path}@${replacement.Version}`);
  }
  return versions;
}

// The version Time on the proxy is the commit's, which whoever tags it can backdate.
// The index lists each version when the proxy first fetches it, so a version in the
// index's recent window is new however old its commit claims to be. Run this after
// `go get` and `go mod tidy`, which fetched every version through the proxy.
export function verdicts(versions, { published, firstSeen, now, minimumMs }) {
  const cutoff = now - minimumMs;
  return [...versions].sort().map((version) => {
    const time = Date.parse(published.get(version));
    const seen = firstSeen.get(version);
    return {
      version,
      published: published.get(version),
      firstSeen: seen,
      tooNew: !(time <= cutoff) || seen !== undefined,
    };
  });
}

// Every module version the proxy first fetched after `since`, in index order.
export async function recentlyIndexed(since, fetchText) {
  const seen = new Map();
  for (let from = since; ; ) {
    const lines = (
      await fetchText(`${index}?since=${from}&limit=${indexPage}`)
    )
      .split("\n")
      .filter(Boolean)
      .map((line) => JSON.parse(line));
    for (const { Path, Version, Timestamp } of lines) {
      const key = `${Path}@${Version}`;
      if (!seen.has(key)) seen.set(key, Timestamp);
    }
    if (lines.length < indexPage) return seen;
    const last = lines.at(-1).Timestamp;
    // A full page of one timestamp would never advance.
    if (last === from) throw new Error(`index.golang.org: stuck at ${from}`);
    from = last;
  }
}

function goModFile(text) {
  const directory = mkdtempSync(join(tmpdir(), "astilba-go-release-age-"));
  try {
    const file = join(directory, "go.mod");
    writeFileSync(file, text);
    return JSON.parse(
      execFileSync("go", ["mod", "edit", "-json", file], { encoding: "utf8" }),
    );
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
}

function baseText(base, path) {
  try {
    return execFileSync("git", ["show", `${base}:${path}`], {
      cwd: root,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    });
  } catch {
    return undefined;
  }
}

// Versions in the working tree's go.mod files that the base commit's did not have.
export function changedVersions(base) {
  const files = execFileSync("git", ["ls-files", "*go.mod"], {
    cwd: root,
    encoding: "utf8",
  })
    .split("\n")
    .filter((path) => path.endsWith("go.mod") && !path.includes("/testdata/"));
  const changed = new Set();
  for (const path of files) {
    const before = baseText(base, path);
    const old = downloadedVersions(
      before === undefined ? undefined : goModFile(before),
    );
    for (const version of downloadedVersions(
      goModFile(readFileSync(join(root, path), "utf8")),
    ))
      if (!old.has(version)) changed.add(version);
  }
  return changed;
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  const base = process.argv[2] ?? "origin/main";
  const minimumMs = minimumAgeMs(
    readFileSync(join(root, "pnpm-workspace.yaml"), "utf8"),
  );
  const versions = changedVersions(base);
  if (versions.size === 0) {
    console.log(`No Go module version changed since ${base}.`);
  } else {
    const fetchText = async (url) => (await download(url)).toString("utf8");
    const published = new Map();
    for (const version of versions) {
      const at = version.lastIndexOf("@");
      const info = JSON.parse(
        await fetchText(
          `${proxy}/${escapeModule(version.slice(0, at))}/@v/${escapeModule(version.slice(at + 1))}.info`,
        ),
      );
      published.set(version, info.Time);
    }
    const now = Date.now();
    const recent = await recentlyIndexed(
      new Date(now - minimumMs).toISOString(),
      fetchText,
    );
    const firstSeen = new Map(
      [...versions]
        .filter((version) => recent.has(version))
        .map((version) => [version, recent.get(version)]),
    );
    const results = verdicts(versions, { published, firstSeen, now, minimumMs });
    for (const { version, published, firstSeen, tooNew } of results)
      console.log(
        `${tooNew ? "too new" : "ok     "}  ${version}  committed ${published}${firstSeen ? `, first fetched by the proxy ${firstSeen}` : ""}`,
      );
    const failed = results.filter(({ tooNew }) => tooNew).length;
    if (failed > 0) {
      console.error(
        `${failed} Go module version(s) are younger than minimumReleaseAge (${minimumMs / 60_000} minutes).`,
      );
      process.exitCode = 1;
    }
  }
}
