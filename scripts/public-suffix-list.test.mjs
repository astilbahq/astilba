import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import test from "node:test";
import { checkCommit, sourceFile } from "./public-suffix-list.mjs";

const sha = "3db7776283b0874634fea8b01168f785360d64f2";
const now = Date.parse("2026-11-11T12:00:00Z");
const day = 86_400_000;

test("a commit on main older than the gate is accepted", () => {
  checkCommit({ sha, committed: "2026-11-10T11:59:59Z", comparison: "ahead", now, minimumMs: day });
  checkCommit({ sha, committed: "2026-11-10T12:00:00Z", comparison: "identical", now, minimumMs: day });
});

test("a commit inside the release-age gate is refused", () => {
  assert.throws(
    () => checkCommit({ sha, committed: "2026-11-10T12:00:01Z", comparison: "ahead", now, minimumMs: day }),
    /inside the release-age gate/,
  );
  assert.throws(
    () => checkCommit({ sha, committed: "not a date", comparison: "ahead", now, minimumMs: day }),
    /inside the release-age gate/,
  );
});

// GitHub serves a fork's commit under the upstream name; only main's history counts.
test("a commit main does not contain is refused", () => {
  for (const comparison of ["behind", "diverged"])
    assert.throws(
      () => checkCommit({ sha, committed: "2026-11-01T00:00:00Z", comparison, now, minimumMs: day }),
      /not on publicsuffix\/list main/,
    );
});

test("only a full commit SHA is pinned", () => {
  assert.throws(
    () => checkCommit({ sha: "main", committed: "2026-11-01T00:00:00Z", comparison: "identical", now, minimumMs: day }),
    /not a full commit SHA/,
  );
});

test("the source file records the commit and both files' hashes", () => {
  const list = Buffer.from("com\n");
  const tests = Buffer.from("checkPublicSuffix('com', null);\n");
  const text = sourceFile({ sha, committed: "2026-11-10T11:23:46Z", list, tests });
  const hash = (bytes) => createHash("sha256").update(bytes).digest("hex");
  assert.match(text, new RegExp(`listCommit    = "${sha}"`));
  assert.match(text, new RegExp(`listSHA256    = "${hash(list)}"`));
  assert.match(text, new RegExp(`testsSHA256   = "${hash(tests)}"`));
  assert.match(text, /^\/\/ Code generated .* DO NOT EDIT\.$/m);
});
