import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname } from "node:path";
import { test } from "node:test";
import {
  findGoTools,
  findImages,
  findReleases,
  newestInLine,
  offTrack,
  parseTracks,
} from "./pin-sources.mjs";

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
  for (const [bad, message] of [
    [{ releases: "^1" }, /must have exactly/],
    [{ ...entry, extra: 1 }, /must have exactly/],
    [{ ...entry, reason: " " }, /needs a reason/],
    [{ ...entry, releases: "(" }, /not a pattern/],
  ])
    assert.throws(() => parseTracks({ images: { x: bad } }), message);
});

test("every track in the repository names a pin and is followed by it", () => {
  const tracked = execFileSync("git", ["ls-files", "-z"], { encoding: "utf8" })
    .split("\0")
    .filter((path) =>
      /\.(go|mjs|sh|tmpl|ya?ml|tsx?)$|(^|\/)(Dockerfile|go\.mod)$/.test(path),
    )
    .flatMap((path) => {
      try {
        return [{ path, text: readFileSync(path, "utf8") }];
      } catch {
        return [];
      }
    });
  const tracks = parseTracks(
    JSON.parse(readFileSync(".github/pinned-releases.json", "utf8")),
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
