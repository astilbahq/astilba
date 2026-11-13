import assert from "node:assert/strict";
import test from "node:test";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { addToReport, chooseCommit, pinnedCommit, sourceFile } from "./public-suffix-list.mjs";

const now = Date.parse("2026-11-11T12:00:00Z");
const day = 86_400_000;
const update = (after, timestamp, extra = {}) => ({
  after,
  timestamp,
  ref: "refs/heads/main",
  activity_type: "push",
  ...extra,
});

test("the newest update to main older than the gate is chosen", () => {
  const chosen = chooseCommit(
    [
      update("c".repeat(40), "2026-11-11T11:00:00Z"),
      update("b".repeat(40), "2026-11-10T12:00:00Z", { activity_type: "pr_merge" }),
      update("a".repeat(40), "2026-11-09T12:00:00Z"),
    ],
    now,
    day,
  );
  assert.equal(chosen.after, "b".repeat(40));
});

// A commit's own dates are chosen by whoever made it; only GitHub's record of the push
// shows how long the commit has been public on main.
test("the gate ignores how old a commit claims to be", () => {
  assert.throws(
    () => chooseCommit([update("c".repeat(40), "2026-11-11T11:59:00Z", { commit: { committer: { date: "2026-01-01T00:00:00Z" } } })], now, day),
    /older than the release-age gate/,
  );
});

test("only updates to main count", () => {
  assert.throws(
    () =>
      chooseCommit(
        [
          update("d".repeat(40), "2026-11-01T00:00:00Z", { ref: "refs/heads/other" }),
          update("0".repeat(40), "2026-11-01T00:00:00Z", { activity_type: "branch_deletion" }),
        ],
        now,
        day,
      ),
    /older than the release-age gate/,
  );
});

test("--check reads the commit that the generated source pins", () => {
  const sha = "3db7776283b0874634fea8b01168f785360d64f2";
  assert.equal(pinnedCommit(sourceFile({ sha, pushed: "2026-11-10T11:23:46Z" })), sha);
  assert.throws(() => pinnedCommit("package publicsuffix\n"), /names no commit/);
});

test("a failed check joins the scan's report, which a scan that crashed never wrote", () => {
  const path = join(mkdtempSync(join(tmpdir(), "psl-report-")), "report.json");
  addToReport(path, new Error("not written"));
  writeFileSync(path, JSON.stringify({ problems: [{ key: "blocked|a", text: "a" }] }));
  addToReport(path, new Error("list.dat differs"));
  assert.deepEqual(JSON.parse(readFileSync(path, "utf8")).problems.at(-1), {
    section: "Public Suffix List",
    key: "public-suffix-list",
    text: "list.dat differs",
  });
});
