import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { test } from "node:test";
import {
  findGoTools,
  findImages,
  findReleases,
  newestInLine,
  offTrack,
  parseTracks,
  retiredKey,
  trackedFiles,
} from "./pin-sources.mjs";

const root = resolve(import.meta.dirname, "..");

const stable = /^\d+\.\d*[02468]\.\d+(-|$)/;
const nginx = [
  "1.30.7-alpine3.24",
  "1.31.8-alpine3.24",
  "1.32.1-alpine3.24",
  "1.33.0-alpine3.24",
];

test("a pin on a track is compared only with releases on that track", () => {
  assert.equal(newestInLine("1.30.6-alpine3.24", nginx), "1.33.0-alpine3.24");
  assert.equal(
    newestInLine("1.30.6-alpine3.24", nginx, stable),
    "1.32.1-alpine3.24",
  );
  assert.equal(newestInLine("1.32.1-alpine3.24", nginx, stable), undefined);
});

test("a pin off its own track is reported, not compared", () => {
  assert.throws(
    () => offTrack("1.31.8-alpine3.24", stable),
    /not on its track/,
  );
  assert.doesNotThrow(() => offTrack("1.30.6-alpine3.24", stable));
  assert.doesNotThrow(() => offTrack("anything", undefined));
});

test("tracks name a pattern and a reason, and nothing else", () => {
  const entry = { releases: "^1\\.", reason: "Why." };
  const tracks = parseTracks({
    images: { nginx: entry },
    tools: { "o/t": entry },
  });
  assert.ok(tracks.images.get("nginx").test("1.2"));
  assert.ok(tracks.tools.get("o/t"));
  assert.equal(parseTracks({}).images.size, 0);
  assert.throws(
    () => parseTracks({ image: { nginx: entry } }),
    /unknown keys: image/,
  );
  for (const [bad, message] of [
    [{ releases: "^1" }, /must have exactly/],
    [{ ...entry, extra: 1 }, /must have exactly/],
    [{ ...entry, reason: " " }, /needs a reason/],
    [{ ...entry, releases: "(" }, /not a pattern/],
    [{ ...entry, releases: 1 }, /needs a releases/],
  ])
    assert.throws(() => parseTracks({ images: { x: bad } }), message);
});

test("a retired pin is keyed image@digest, with a reason", () => {
  const key = `postgres@sha256:${"a".repeat(64)}`;
  const tracks = parseTracks({ retired: { [key]: "Replaced." } });
  assert.equal(
    tracks.retired.get(
      retiredKey({ image: "postgres", digest: `sha256:${"a".repeat(64)}` }),
    ),
    "Replaced.",
  );
  assert.equal(parseTracks({}).retired.size, 0);
  for (const [bad, message] of [
    [{ "postgres:18.4": "Replaced." }, /must be image@sha256:digest/],
    [{ [key]: " " }, /needs a reason/],
    [{ [key]: 1 }, /needs a reason/],
  ])
    assert.throws(() => parseTracks({ retired: bad }), message);
});

test("every retired pin in the repository is still pinned", () => {
  const tracks = parseTracks(
    JSON.parse(
      readFileSync(resolve(root, ".github/pinned-releases.json"), "utf8"),
    ),
  );
  const pinned = new Set(findImages(trackedFiles(root)).map(retiredKey));
  for (const key of tracks.retired.keys())
    assert.ok(pinned.has(key), `${key} is retired but no longer pinned`);
});

test("every track in the repository names a pin and is followed by it", () => {
  // The same files the report reads.
  const tracked = trackedFiles(root);
  const tracks = parseTracks(
    JSON.parse(
      readFileSync(resolve(root, ".github/pinned-releases.json"), "utf8"),
    ),
  );
  const images = findImages(tracked);
  for (const [name, track] of tracks.images) {
    const pinned = images.filter(({ image }) => image === name);
    assert.ok(pinned.length, `${name} is not pinned`);
    for (const { tag } of pinned) offTrack(tag, track);
  }
  const tools = [
    ...findGoTools(
      tracked
        .filter(({ path }) => /(^|\/)go\.mod$/.test(path))
        .map(({ path, text }) => ({ dir: dirname(path), text })),
    ).map(({ module, version }) => [module, version]),
    ...findReleases(tracked).map(({ repository, version }) => [
      repository,
      version,
    ]),
  ];
  for (const [name, track] of tracks.tools) {
    const pinned = tools.filter(([tool]) => tool === name);
    assert.ok(pinned.length, `${name} is not pinned`);
    for (const [, version] of pinned) offTrack(version, track);
  }
});
