import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { addedReleases, cooldownMs, releaseTime, verdicts } from "./release-age.mjs";

const day = 86_400_000;

test("the gate is the Docker cooldown in dependabot.yml", () => {
  const text = `version: 2
updates:
  - package-ecosystem: npm
    cooldown:
      default-days: 2
  - package-ecosystem: docker
    directory: /
    cooldown:
      default-days: 7
  - package-ecosystem: github-actions
    cooldown:
      default-days: 3
`;
  assert.equal(cooldownMs(text), 7 * day);
  assert.throws(
    () => cooldownMs("updates:\n  - package-ecosystem: docker\n    directory: /\n"),
    /no cooldown for docker/,
  );
  assert.equal(
    cooldownMs(readFileSync(new URL("../.github/dependabot.yml", import.meta.url), "utf8")),
    7 * day,
  );
});

test("only a release the base did not download is checked", () => {
  const before = [{ repository: "moby/buildkit", version: "v0.34.1", paths: ["a.sh"] }];
  const after = [
    { repository: "moby/buildkit", version: "v0.35.0", paths: ["a.sh"] },
    { repository: "jdx/mise", version: "v2026.10.14", paths: ["a.sh"] },
  ];
  assert.deepEqual(addedReleases(before, after), [after[0], after[1]]);
  assert.deepEqual(addedReleases(after, after), []);
});

test("a replaced asset makes a release as new as the replacement", () => {
  const published_at = "2026-11-01T00:00:00Z";
  assert.equal(releaseTime({ tag_name: "v1", published_at, assets: [] }), Date.parse(published_at));
  assert.equal(
    releaseTime({
      tag_name: "v1",
      published_at,
      assets: [{ updated_at: "2026-10-31T23:00:00Z" }, { updated_at: "2026-11-10T00:00:00Z" }],
    }),
    Date.parse("2026-11-10T00:00:00Z"),
  );
  assert.throws(() => releaseTime({ tag_name: "v2", assets: [] }), /no publication time/);
  assert.throws(() => releaseTime({ tag_name: "v2", draft: true, published_at }), /draft or pre-release/);
  assert.throws(() => releaseTime({ tag_name: "v2", prerelease: true, published_at }), /draft or pre-release/);
});

test("a release younger than the gate fails, and one exactly as old passes", () => {
  const now = Date.parse("2026-11-14T12:00:00Z");
  const releases = [
    { repository: "a/old", version: "v1", paths: [] },
    { repository: "a/edge", version: "v1", paths: [] },
    { repository: "a/new", version: "v1", paths: [] },
    { repository: "a/unknown", version: "v1", paths: [] },
  ];
  const times = new Map([
    ["a/old@v1", now - 30 * day],
    ["a/edge@v1", now - 7 * day],
    ["a/new@v1", now - 3 * day],
    ["a/unknown@v1", Number.NaN],
  ]);
  assert.deepEqual(
    verdicts(releases, { times, now, minimumMs: 7 * day }).map(({ tooNew }) => tooNew),
    [false, false, true, true],
  );
});
