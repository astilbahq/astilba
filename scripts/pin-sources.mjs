import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// Finds the pins scripts/pin-report.mjs checks, from the repository's own files, so a
// pin added later needs no list.

// name[:tag]@sha256:digest, where a first component with a dot, a colon or "localhost"
// names the registry. Markdown is left out: it describes pins rather than using them.
const reference =
  /(?<![\w./:@-])((?:(?:[a-z0-9-]+(?:\.[a-z0-9-]+)+|localhost)(?::\d+)?\/)?[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:\/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*):([\w][\w.-]{0,127})@sha256:([0-9a-f]{64})(?![0-9a-f])/g;

export function parseName(name) {
  const [first, ...rest] = name.split("/");
  const host =
    rest.length && (/[.:]/.test(first) || first === "localhost")
      ? first
      : undefined;
  let repository = host ? rest.join("/") : name;
  const registry =
    host === "index.docker.io" ? "docker.io" : (host ?? "docker.io");
  if (registry === "docker.io" && !repository.includes("/"))
    repository = `library/${repository}`;
  const image =
    registry === "docker.io"
      ? repository.replace(/^library\//, "")
      : `${registry}/${repository}`;
  return { registry, repository, image };
}

// Code that ships in the server image or runs on hosts, as opposed to CI and tests.
const shipped = (path) =>
  path === "Dockerfile" ||
  ((path.startsWith("services/platform/") ||
    path.startsWith("apps/platform/src/")) &&
    !/(_test\.go|\.test\.[jt]sx?|\.stories\.[jt]sx?)$/.test(path) &&
    !path.includes("/testdata/"));

// Every digest-pinned image reference in the given files, one entry per digest.
export function findImages(files) {
  const pins = new Map();
  for (const { path, text } of files) {
    if (path.endsWith(".md")) continue;
    for (const [, name, tag, digest] of text.matchAll(reference)) {
      const parsed = parseName(name);
      const key = `${parsed.registry}/${parsed.repository}@${digest}`;
      const pin = pins.get(key) ?? {
        ...parsed,
        tag,
        digest: `sha256:${digest}`,
        paths: new Set(),
      };
      pin.paths.add(path);
      pins.set(key, pin);
    }
  }
  return [...pins.values()]
    .map(({ paths, ...pin }) => ({
      ...pin,
      paths: [...paths].sort(),
      runtime: [...paths].some(shipped),
    }))
    .toSorted(
      (a, b) =>
        b.runtime - a.runtime ||
        a.image.localeCompare(b.image) ||
        a.digest.localeCompare(b.digest),
    );
}

// The Go tools each module runs with `go tool`, with the module and version that
// provide them, from the module's go.mod.
export function findGoTools(modules) {
  const tools = [];
  for (const { dir, text } of modules) {
    const plain = text.replace(/\/\/.*$/gm, "");
    const block = (keyword) =>
      [
        ...[
          ...plain.matchAll(new RegExp(`^${keyword}\\s*\\(([^)]*)\\)`, "gm")),
        ].flatMap(([, body]) => body.split("\n")),
        ...[
          ...plain.matchAll(new RegExp(`^${keyword}\\s+([^(\\s].*)$`, "gm")),
        ].map(([, line]) => line),
      ]
        .map((line) => line.trim().split(/\s+/))
        .filter(([first]) => first);
    const requires = block("require");
    for (const [tool] of block("tool")) {
      const [path, version] =
        requires
          .filter(([path]) => tool === path || tool.startsWith(`${path}/`))
          .toSorted((a, b) => b[0].length - a[0].length)[0] ?? [];
      if (!path)
        throw new Error(`${dir}/go.mod: no requirement provides ${tool}`);
      tools.push({ dir, tool, module: path, version });
    }
  }
  return tools;
}

// Release binaries fetched from GitHub with the version written in the URL or in a
// constant of the same file.
const releaseDownload =
  /https:\/\/github\.com\/([\w.-]+\/[\w.-]+)\/releases\/download\/(v?)(?:\$\{(\w+)\}|([0-9][\w.+-]*))\//g;

export function findReleases(files) {
  const releases = new Map();
  for (const { path, text } of files) {
    if (path.endsWith(".md")) continue;
    for (const [, repository, prefix, constant, literal] of text.matchAll(
      releaseDownload,
    )) {
      const version =
        literal ??
        new RegExp(
          `\\b(?:const|let|var)\\s+${constant}\\s*=\\s*"([^"]+)"`,
        ).exec(text)?.[1];
      if (!version)
        throw new Error(`${path}: cannot read the version of ${repository}`);
      const key = `${repository}@${prefix}${version}`;
      const release = releases.get(key) ?? {
        repository,
        version: `${prefix}${version}`,
        paths: new Set(),
      };
      release.paths.add(path);
      releases.set(key, release);
    }
  }
  return [...releases.values()].map(({ paths, ...release }) => ({
    ...release,
    paths: [...paths].sort(),
  }));
}

// A version's line is its shape with the numbers taken out, and its first number
// unless that is a year. So 18.4-alpine3.23 can become 18.5-alpine3.24, but not 19.0
// or 18.5-trixie; v2026.10.14 can become any later date.
function versionParts(version) {
  const parts = version.split(/(\d+)/);
  return {
    shape: parts.filter((_, index) => index % 2 === 0).join("#"),
    numbers: parts.filter((_, index) => index % 2).map(Number),
  };
}

function compareNumbers(a, b) {
  for (let index = 0; index < a.length; index++)
    if (a[index] !== b[index]) return a[index] - b[index];
  return 0;
}

//
// A pin can follow a narrower track than its line, such as nginx's stable releases,
// whose minor version is even; then only releases on that track count.
export function newestInLine(current, candidates, track) {
  const mine = versionParts(current);
  let best;
  for (const candidate of candidates) {
    if (track && !track.test(candidate)) continue;
    const other = versionParts(candidate);
    if (
      other.shape !== mine.shape ||
      other.numbers.length !== mine.numbers.length ||
      (mine.numbers[0] < 1000 && other.numbers[0] !== mine.numbers[0]) ||
      compareNumbers(other.numbers, mine.numbers) <= 0
    )
      continue;
    if (!best || compareNumbers(other.numbers, best.numbers) > 0)
      best = { version: candidate, numbers: other.numbers };
  }
  return best?.version;
}

const trackFields = ["reason", "releases"];

// .github/pinned-releases.json names the images and tools that follow a narrower
// track than their line: a pattern every release on the track matches, and why. Its
// retired list names image pins kept on purpose after a newer release replaced them,
// such as a PostgreSQL release that databases and backups made under it still name,
// each keyed image@digest with the reason.
export function parseTracks(document) {
  const unknown = Object.keys(document).filter(
    (key) => !["images", "tools", "retired"].includes(key),
  );
  if (unknown.length)
    throw new Error(
      `pinned-releases.json has unknown keys: ${unknown.join(", ")}`,
    );
  const tracks = {};
  for (const kind of ["images", "tools"]) {
    tracks[kind] = new Map();
    for (const [name, entry] of Object.entries(document[kind] ?? {})) {
      const label = `pinned-releases.json ${kind} ${name}`;
      if (Object.keys(entry).toSorted().join() !== trackFields.join())
        throw new Error(
          `${label} must have exactly: ${trackFields.join(", ")}`,
        );
      for (const field of trackFields)
        if (typeof entry[field] !== "string" || !entry[field].trim())
          throw new Error(`${label} needs a ${field}`);
      let pattern;
      try {
        pattern = new RegExp(entry.releases);
      } catch (error) {
        throw new Error(`${label} releases is not a pattern: ${error.message}`);
      }
      tracks[kind].set(name, pattern);
    }
  }
  tracks.retired = new Map();
  for (const [key, reason] of Object.entries(document.retired ?? {})) {
    if (!/^[^\s@]+@sha256:[0-9a-f]{64}$/.test(key))
      throw new Error(
        `pinned-releases.json retired ${key} must be image@sha256:digest`,
      );
    if (typeof reason !== "string" || !reason.trim())
      throw new Error(`pinned-releases.json retired ${key} needs a reason`);
    tracks.retired.set(key, reason);
  }
  return tracks;
}

// The key a retired pin is listed under.
export const retiredKey = (pin) => `${pin.image}@${pin.digest}`;

// A track runs dry when upstream stops building it, as for an old Alpine line. A newer
// release off the track counts only if that release would be on the track in the pin's own
// form: 18.8-alpine3.24 does for 18.7-alpine3.23, because 18.8-alpine3.23 would match, but
// nginx's mainline 1.31.8 never does for a stable track, whatever the suffix.
export function beyondTrack(pinTag, candidate, track) {
  const lead = (tag) => /^[0-9.]+/.exec(tag)?.[0] ?? "";
  const version = lead(candidate);
  if (!track || !version || version === lead(pinTag)) return false;
  return track.test(version + pinTag.slice(lead(pinTag).length));
}

// The newest release beyond a pin's track that the track would take in the pin's form, when it
// is newer than anything on the track: the sign that the track has run dry.
export function offTrackRelease(pinTag, tags, track, newer) {
  const beyond = newestInLine(
    pinTag,
    tags.filter((tag) => beyondTrack(pinTag, tag, track)),
  );
  return beyond && beyondTrack(newer ?? pinTag, beyond, track)
    ? beyond
    : undefined;
}

// A pin off its own track is a mistake in the pin or the track, so it is reported
// rather than compared.
export function offTrack(current, track) {
  if (track && !track.test(current))
    throw new Error(`${current} is not on its track ${track}`);
}

// Every tracked text file, which is what the pins are found in.
export function trackedFiles(root) {
  return execFileSync("git", ["ls-files", "-z"], {
    cwd: root,
    encoding: "utf8",
  })
    .split("\0")
    .filter(Boolean)
    .flatMap((path) => {
      try {
        const bytes = readFileSync(join(root, path));
        return bytes.includes(0)
          ? []
          : [{ path, text: bytes.toString("utf8") }];
      } catch {
        // A path deleted from the working tree but still in the index.
        return [];
      }
    });
}
