import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { download } from "./fetch-attempts.mjs";
import { findReleases, trackedFiles } from "./pin-sources.mjs";

// Release binaries downloaded from GitHub wait as long as images and actions do, so a
// bad release has time to be caught: the Docker cooldown in .github/dependabot.yml.
// This applies that gate to every release a branch's files gained since a base commit.
// See docs/continuous-integration.md#pinned-images-and-tools.

const root = resolve(import.meta.dirname, "..");

// The days of the Docker entry's cooldown, so the image and binary gates cannot drift.
export function minimumAgeMs(dependabotText) {
  const entries = dependabotText.split(/^ {2}- package-ecosystem:/m).slice(1);
  const docker = entries.find((entry) => /^\s*docker\s*$/m.test(entry.split("\n")[0]));
  const days = docker && /^\s+default-days:\s*(\d+)\s*$/m.exec(docker)?.[1];
  if (!days) throw new Error(".github/dependabot.yml: no cooldown for docker");
  return Number(days) * 86_400_000;
}

const key = ({ repository, version }) => `${repository}@${version}`;

// Releases the working tree downloads that the base commit did not.
export function addedReleases(before, after) {
  const old = new Set(before.map(key));
  return after.filter((release) => !old.has(key(release)));
}

// When a release became what a download fetches: published, or later if an asset was
// replaced since. A draft or pre-release is never pinned.
export function releaseTime(release) {
  if (release.draft || release.prerelease)
    throw new Error(`${release.tag_name} is a draft or pre-release`);
  const times = [release.published_at, ...(release.assets ?? []).map(({ updated_at }) => updated_at)].map(
    (time) => Date.parse(time),
  );
  if (times.some(Number.isNaN)) throw new Error(`${release.tag_name} has no publication time`);
  return Math.max(...times);
}

export function verdicts(releases, { times, now, minimumMs }) {
  return releases.map((release) => {
    const time = times.get(key(release));
    return {
      ...release,
      released: Number.isFinite(time) ? new Date(time).toISOString() : "at an unknown time",
      tooNew: !(time <= now - minimumMs),
    };
  });
}

const downloads = "/releases/download/";

function baseFiles(base) {
  const listed = execFileSync("git", ["grep", "-l", "-z", "-F", downloads, base, "--"], {
    cwd: root,
    encoding: "utf8",
  }).split("\0");
  return listed
    .filter(Boolean)
    .map((entry) => entry.slice(base.length + 1))
    .map((path) => ({
      path,
      text: execFileSync("git", ["show", `${base}:${path}`], { cwd: root, encoding: "utf8" }),
    }));
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const base = process.argv[2] ?? "origin/main";
  const minimumMs = minimumAgeMs(readFileSync(join(root, ".github/dependabot.yml"), "utf8"));
  const current = trackedFiles(root).filter(({ text }) => text.includes(downloads));
  const releases = addedReleases(findReleases(baseFiles(base)), findReleases(current));
  if (releases.length === 0) {
    console.log(`No release binary changed since ${base}.`);
  } else {
    const headers = {
      accept: "application/vnd.github+json",
      ...(process.env.GITHUB_TOKEN && { authorization: `Bearer ${process.env.GITHUB_TOKEN}` }),
    };
    const times = new Map();
    for (const release of releases) {
      const body = await download(
        `https://api.github.com/repos/${release.repository}/releases/tags/${encodeURIComponent(release.version)}`,
        { headers },
      );
      times.set(key(release), releaseTime(JSON.parse(body.toString("utf8"))));
    }
    const results = verdicts(releases, { times, now: Date.now(), minimumMs });
    for (const { repository, version, released, tooNew, paths } of results)
      console.log(
        `${tooNew ? "too new" : "ok     "}  ${repository} ${version}  released ${released}  (${paths.join(", ")})`,
      );
    const failed = results.filter(({ tooNew }) => tooNew).length;
    if (failed > 0) {
      console.error(
        `${failed} release binary version(s) are younger than ${minimumMs / 86_400_000} days, the Docker cooldown in .github/dependabot.yml.`,
      );
      process.exitCode = 1;
    }
  }
}
