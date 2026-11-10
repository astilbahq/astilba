import assert from "node:assert/strict";
import test from "node:test";
import {
  downloadedVersions,
  escapeModule,
  minimumAgeMs,
  recentlyIndexed,
  verdicts,
} from "./go-release-age.mjs";

test("capitals in a module path are escaped as the proxy expects", () => {
  assert.equal(
    escapeModule("github.com/BurntSushi/toml"),
    "github.com/!burnt!sushi/toml",
  );
});

test("the gate is pnpm's minimumReleaseAge", () => {
  assert.equal(minimumAgeMs("packages: []\nminimumReleaseAge: 1440\n"), 86_400_000);
  assert.throws(() => minimumAgeMs("packages: []\n"), /no minimumReleaseAge/);
});

test("a replacement is checked instead of what it replaces", () => {
  const versions = downloadedVersions({
    Require: [
      { Path: "example.test/a", Version: "v1.0.0" },
      { Path: "example.test/b", Version: "v1.2.0" },
      { Path: "example.test/c", Version: "v0.1.0" },
      { Path: "example.test/d", Version: "v2.0.0" },
    ],
    Replace: [
      { Old: { Path: "example.test/b" }, New: { Path: "example.test/fork", Version: "v1.2.1" } },
      { Old: { Path: "example.test/c" }, New: { Path: "../c" } },
      { Old: { Path: "example.test/d", Version: "v1.0.0" }, New: { Path: "../d" } },
    ],
  });
  assert.deepEqual(
    [...versions],
    ["example.test/a@v1.0.0", "example.test/fork@v1.2.1", "example.test/d@v2.0.0"],
  );
  assert.deepEqual([...downloadedVersions(undefined)], []);
});

const now = Date.parse("2026-11-10T12:00:00Z");
const minimumMs = 86_400_000;

test("a version is too new by its commit time or by when the proxy first saw it", () => {
  const result = verdicts(
    ["example.test/old@v1.0.0", "example.test/fresh@v1.0.0", "example.test/backdated@v1.0.0"],
    {
      published: new Map([
        ["example.test/old@v1.0.0", "2026-11-01T00:00:00Z"],
        ["example.test/fresh@v1.0.0", "2026-11-10T00:00:00Z"],
        ["example.test/backdated@v1.0.0", "2026-01-01T00:00:00Z"],
      ]),
      firstSeen: new Map([["example.test/backdated@v1.0.0", "2026-11-10T11:00:00Z"]]),
      now,
      minimumMs,
    },
  );
  assert.deepEqual(
    result.map(({ version, tooNew }) => [version, tooNew]),
    [
      ["example.test/backdated@v1.0.0", true],
      ["example.test/fresh@v1.0.0", true],
      ["example.test/old@v1.0.0", false],
    ],
  );
});

test("a version without a commit time is too new", () => {
  const [result] = verdicts(["example.test/a@v1.0.0"], {
    published: new Map(),
    firstSeen: new Map(),
    now,
    minimumMs,
  });
  assert.equal(result.tooNew, true);
});

const line = (Path, Version, Timestamp) => JSON.stringify({ Path, Version, Timestamp });

test("the index is read page by page from the window's start", async () => {
  const requested = [];
  const pages = [
    Array.from({ length: 2000 }, (_, n) =>
      line(`example.test/m${n}`, "v1.0.0", `2026-11-09T12:00:${String(n % 60).padStart(2, "0")}Z`),
    ).join("\n"),
    `${line("example.test/m0", "v1.0.0", "2026-11-10T01:00:00Z")}\n${line("example.test/last", "v0.1.0", "2026-11-10T02:00:00Z")}\n`,
  ];
  const seen = await recentlyIndexed("2026-11-09T12:00:00Z", async (url) => {
    requested.push(url);
    return pages[requested.length - 1];
  });
  assert.deepEqual(requested, [
    "https://index.golang.org/index?since=2026-11-09T12:00:00Z&limit=2000",
    "https://index.golang.org/index?since=2026-11-09T12:00:19Z&limit=2000",
  ]);
  assert.equal(seen.get("example.test/m0@v1.0.0"), "2026-11-09T12:00:00Z");
  assert.equal(seen.get("example.test/last@v0.1.0"), "2026-11-10T02:00:00Z");
});

test("a full index page that cannot advance stops", async () => {
  const page = Array.from({ length: 2000 }, (_, n) =>
    line(`example.test/m${n}`, "v1.0.0", "2026-11-09T12:00:00Z"),
  ).join("\n");
  await assert.rejects(
    recentlyIndexed("2026-11-09T12:00:00Z", async () => page),
    /stuck/,
  );
});
