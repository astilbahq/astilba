import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { readContentHistory } from "../src/docs/content-history.ts";

const app = fileURLToPath(new URL("../", import.meta.url));
const publicRepository = resolve(process.argv[2]);
const sourceRevision = "69101c58b1494689a3ec48ebd3e059f5d33ce729";
const importCommit = "a257d04d3e32db5025bac2f8aaa1ed0d0470f09a";
const sourceGit = (args) =>
  execFileSync("git", ["--git-dir", publicRepository, ...args], {
    encoding: "utf8",
  });
const sourceBytes = (revision, path) =>
  execFileSync("git", [
    "--git-dir",
    publicRepository,
    "show",
    `${revision}:${path}`,
  ]);
assert.equal(
  sourceGit(["rev-parse", "--is-shallow-repository"]).trim(),
  "false"
);
assert.equal(sourceGit(["rev-parse", sourceRevision]).trim(), sourceRevision);
const paths = sourceGit([
  "ls-tree",
  "-r",
  "--name-only",
  "-z",
  sourceRevision,
  "--",
  "src/content/docs",
])
  .split("\0")
  .filter((path) => /\.mdx?$/u.test(path));
const pages = Object.fromEntries(
  paths.map((path) => {
    const bytes = sourceBytes(sourceRevision, path);
    assert.deepEqual(
      bytes,
      execFileSync("git", ["show", `${importCommit}:apps/docs/${path}`], {
        cwd: app,
      }),
      path
    );
    const change = readContentHistory(path, sourceRevision, app, (args) =>
      sourceGit(args)
    ).find(({ before, after }) => before !== after);
    assert.ok(change, `No public content history: ${path}`);
    assert.deepEqual(bytes, sourceBytes(change.commit, change.path), path);
    // All pinned public pages retain their original path at their last content edit.
    assert.equal(change.path, path);
    return [
      path,
      {
        contentSha256: createHash("sha256").update(bytes).digest("hex"),
        lastModified: change.date,
        sourceCommit: change.commit,
      },
    ];
  })
);
const generated = {
  version: 1,
  repository: "astilbahq/docs",
  sourceRevision,
  importCommit,
  pages,
};
if (process.argv[3] === "--generate") {
  process.stdout.write(`${JSON.stringify(generated, null, 2)}\n`);
} else {
  assert.deepEqual(
    JSON.parse(
      readFileSync(
        new URL("../src/docs/content-provenance.json", import.meta.url),
        "utf8"
      )
    ),
    generated
  );
  process.stdout.write(
    `Verified ${paths.length} public page fingerprints, dates and immutable source commits.\n`
  );
}
