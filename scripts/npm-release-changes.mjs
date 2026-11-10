import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { download } from "./fetch-attempts.mjs";

// Lists every package version pnpm-lock.yaml gained since a base commit, with what
// changed about who publishes it and what it runs on install. pnpm's own
// minimumReleaseAge already holds back young versions. See
// .claude/skills/upgrade-dependencies/SKILL.md.

const root = resolve(import.meta.dirname, "..");
const registry = "https://registry.npmjs.org";
const installScripts = ["preinstall", "install", "postinstall"];

// The name and version of each entry under `packages:`, which pnpm writes without
// peer suffixes.
export function lockedVersions(lockfile) {
  const versions = new Map();
  const section = /^packages:\n([\s\S]*?)(?=^\S|(?![\s\S]))/m.exec(lockfile)?.[1];
  for (const [, key] of (section ?? "").matchAll(/^ {2}'?([^\s':]+)'?:$/gm)) {
    const at = key.lastIndexOf("@");
    const name = key.slice(0, at);
    if (!versions.has(name)) versions.set(name, new Set());
    versions.get(name).add(key.slice(at + 1));
  }
  return versions;
}

const parts = (version) =>
  version
    .split(/[-+]/)[0]
    .split(".")
    .map((part) => Number(part));

// Highest first by release numbers; a prerelease ranks as its release.
export function newestFirst(a, b) {
  const [x, y] = [parts(a), parts(b)];
  for (let i = 0; i < 3; i++) if (x[i] !== y[i]) return y[i] - x[i];
  return 0;
}

// Each version the base did not lock, beside the newest version of the same package
// that the base did lock, if any.
export function addedVersions(base, current) {
  const added = [];
  for (const [name, versions] of current) {
    const old = base.get(name) ?? new Set();
    const previous = [...old].sort(newestFirst)[0];
    for (const version of [...versions].sort(newestFirst))
      if (!old.has(version)) added.push({ name, version, previous });
  }
  return added.sort((a, b) => a.name.localeCompare(b.name));
}

const scriptsOf = (manifest) => [
  ...installScripts.filter((name) => manifest.scripts?.[name]),
  ...(manifest.gypfile ? ["binding.gyp"] : []),
];
const maintainersOf = (manifest) =>
  (manifest.maintainers ?? []).map(({ name }) => name);

// What a reviewer should look at, comparing a version's registry manifest with the
// previous locked version's. Without a previous version, everything about the
// package is new.
export function concerns(next, previous) {
  const found = [];
  const scripts = scriptsOf(next);
  const hadScripts = previous ? scriptsOf(previous) : [];
  const newScripts = scripts.filter((name) => !hadScripts.includes(name));
  if (newScripts.length > 0)
    found.push(`runs ${newScripts.join(", ")} on install`);
  if (!previous) {
    found.push(
      `new package, maintained by ${maintainersOf(next).join(", ") || "nobody listed"}`,
    );
  } else {
    const publisher = next._npmUser?.name;
    if (publisher !== previous._npmUser?.name)
      found.push(
        `published by ${publisher ?? "nobody listed"}, previously ${previous._npmUser?.name ?? "nobody listed"}`,
      );
    const added = maintainersOf(next).filter(
      (name) => !maintainersOf(previous).includes(name),
    );
    if (added.length > 0) found.push(`new maintainers ${added.join(", ")}`);
    if (previous.dist?.attestations && !next.dist?.attestations)
      found.push("no provenance, which the previous version had");
  }
  if (next.deprecated) found.push(`deprecated: ${next.deprecated}`);
  return found;
}

const manifestUrl = (name, version) =>
  `${registry}/${name.replace("/", "%2f")}/${encodeURIComponent(version)}`;

function lockfileAt(base) {
  return execFileSync("git", ["show", `${base}:pnpm-lock.yaml`], {
    cwd: root,
    encoding: "utf8",
    maxBuffer: 256 * 1024 * 1024,
  });
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  const base = process.argv[2] ?? "origin/main";
  const added = addedVersions(
    lockedVersions(lockfileAt(base)),
    lockedVersions(readFileSync(join(root, "pnpm-lock.yaml"), "utf8")),
  );
  if (added.length === 0) console.log(`pnpm-lock.yaml gained no package versions since ${base}.`);
  const manifest = async (name, version) =>
    JSON.parse((await download(manifestUrl(name, version))).toString("utf8"));
  let flagged = 0;
  // A few at a time keeps a large bump quick without crowding the registry.
  for (let start = 0; start < added.length; start += 8) {
    const batch = added.slice(start, start + 8);
    const results = await Promise.all(
      batch.map(async ({ name, version, previous }) =>
        concerns(
          await manifest(name, version),
          previous && (await manifest(name, previous)),
        ),
      ),
    );
    batch.forEach(({ name, version, previous }, n) => {
      if (results[n].length > 0) flagged++;
      console.log(
        `${results[n].length > 0 ? "check" : "ok   "}  ${name} ${previous ? `${previous} -> ` : ""}${version}${results[n].map((text) => `\n         ${text}`).join("")}`,
      );
    });
  }
  if (added.length > 0)
    console.log(`\n${added.length} added, ${flagged} to check against their release notes.`);
}
