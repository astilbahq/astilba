import assert from "node:assert/strict";
import { test } from "node:test";
import { outcome, ownRuns, stopped, windowMs } from "./schedule-watch.mjs";

const now = Date.parse("2026-11-13T13:00:00Z");
const hoursAgo = (hours) => new Date(now - hours * 3_600_000).toISOString();
const repository = "o/r";
const run = (hours, fields = {}) => ({
  id: hours,
  event: "schedule",
  head_branch: "main",
  head_repository: { full_name: repository },
  created_at: hoursAgo(hours),
  html_url: `https://github.test/runs/${hours}`,
  status: "completed",
  conclusion: "success",
  ...fields,
});
const active = { state: "active" };
const window = windowMs / 3_600_000;
const keys = (result) => result.problems.map(({ key }) => key);
const names = ["Report to issues", "Check the lane budget"];
const jobs = (...conclusions) =>
  names.map((name, index) => ({ name, conclusion: conclusions[index] }));

test("only the repository's own scheduled runs on main since the file changed count", () => {
  const runs = [
    run(1, { event: "workflow_dispatch" }),
    run(2, { event: "pull_request" }),
    run(3, { head_repository: { full_name: "fork/r" } }),
    run(4, { head_branch: "feature" }),
    run(30),
    run(5),
    run(50),
  ];
  assert.deepEqual(
    ownRuns(runs, repository, hoursAgo(40)).map(({ id }) => id),
    [5, 30],
  );
});

test("a run whose jobs succeeded is fine; one that failed, timed out or could not start is not", () => {
  assert.equal(outcome(run(2), jobs("success", "success"), names), null);
  assert.match(
    outcome(
      run(2, { conclusion: "failure" }),
      jobs("failure", "success"),
      names,
    ).line,
    /^- Report to issues ended failure: /,
  );
  // A job past its time limit ends cancelled, and so does its run.
  assert.match(
    outcome(
      run(2, { conclusion: "cancelled" }),
      jobs("cancelled", "success"),
      names,
    ).line,
    /Report to issues ended cancelled/,
  );
  assert.match(
    outcome(run(2), [], names).line,
    /Report to issues ended without running; Check the lane budget ended without running/,
  );
  assert.equal(
    outcome(run(2, { conclusion: "startup_failure" }), [], names).key,
    "startup failure",
  );
});

test("a run cancelled before its jobs ran says nothing", () => {
  assert.equal(
    outcome(run(2, { conclusion: "cancelled" }), [], names),
    undefined,
  );
  assert.equal(
    outcome(run(2, { conclusion: "cancelled" }), jobs("skipped", null), names),
    undefined,
  );
});

test("a judged run within the window settles it, a failure included", () => {
  assert.deepEqual(
    stopped({
      workflow: active,
      changed: hoursAgo(500),
      runs: [run(30)],
      judged: { run: run(30), problem: null },
      now,
    }),
    { problems: [], undecided: false },
  );
  const problem = { key: "job failed", line: "- failed" };
  assert.deepEqual(
    keys(
      stopped({
        workflow: active,
        changed: hoursAgo(500),
        runs: [run(2)],
        judged: { run: run(2), problem },
        now,
      }),
    ),
    ["job failed"],
  );
});

test("no judged run for 36 hours, or none since the file changed that long ago, is a problem", () => {
  const stale = stopped({
    workflow: active,
    changed: hoursAgo(500),
    runs: [run(window + 1)],
    judged: { run: run(window + 1), problem: null },
    now,
  });
  assert.deepEqual(keys(stale), ["no run"]);
  const never = stopped({
    workflow: active,
    changed: hoursAgo(window + 1),
    runs: [],
    now,
  });
  assert.match(
    never.problems[0].line,
    /since the workflow last changed on main/,
  );
  // Changed recently, with no run yet: nothing to say.
  assert.deepEqual(
    stopped({ workflow: active, changed: hoursAgo(5), runs: [], now }),
    { problems: [], undecided: true },
  );
});

test("a fresh run still going leaves the issue as it is", () => {
  const result = stopped({
    workflow: active,
    changed: hoursAgo(500),
    runs: [
      run(1, { status: "in_progress", conclusion: null }),
      run(window + 5),
    ],
    judged: { run: run(window + 5), problem: null },
    now,
  });
  assert.deepEqual(result, { problems: [], undecided: true });
});

test("a disabled workflow is a problem even with recent runs", () => {
  assert.deepEqual(
    keys(
      stopped({
        workflow: { state: "disabled_inactivity" },
        changed: hoursAgo(500),
        runs: [run(2)],
        judged: { run: run(2), problem: null },
        now,
      }),
    ),
    ["disabled"],
  );
});
