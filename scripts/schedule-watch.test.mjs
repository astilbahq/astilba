import assert from "node:assert/strict";
import { test } from "node:test";
import { stopped, windowMs } from "./schedule-watch.mjs";

const now = Date.parse("2026-11-13T13:00:00Z");
const hoursAgo = (hours) => new Date(now - hours * 3_600_000).toISOString();
const workflow = (fields = {}) => ({
  state: "active",
  created_at: hoursAgo(24 * 30),
  ...fields,
});
const run = (hours, fields = {}) => ({
  id: hours,
  created_at: hoursAgo(hours),
  html_url: `https://github.test/runs/${hours}`,
  status: "completed",
  conclusion: "success",
  ...fields,
});
const job = "Report to issues";
const passed = [{ name: job, conclusion: "success" }];
const keys = (result) => result.problems.map(({ key }) => key);
const window = windowMs / 3_600_000;

test("a recent run whose report job succeeded is fine, however late GitHub started it", () => {
  const result = stopped({
    workflow: workflow(),
    runs: [run(30)],
    jobs: passed,
    job,
    now,
  });
  assert.deepEqual(keys(result), []);
  assert.equal(result.undecided, false);
});

test("a workflow added within the window with no run yet decides nothing", () => {
  assert.deepEqual(
    stopped({ workflow: workflow({ created_at: hoursAgo(5) }), runs: [], now }),
    { problems: [], undecided: true },
  );
});

test("no run for a day and a half, or ever since it was added, is a problem", () => {
  assert.deepEqual(
    keys(stopped({ workflow: workflow(), runs: [run(window + 1)], now })),
    ["no run"],
  );
  const never = stopped({ workflow: workflow(), runs: [], now });
  assert.deepEqual(keys(never), ["no run"]);
  assert.match(never.problems[0].line, /No run since it was added/);
});

test("a disabled workflow is a problem even with recent runs", () => {
  assert.deepEqual(
    keys(
      stopped({
        workflow: workflow({ state: "disabled_inactivity" }),
        runs: [run(2)],
        now,
      }),
    ),
    ["disabled"],
  );
});

test("a run that could not start, or whose report job failed, is a problem", () => {
  const runs = (conclusion) => [run(2, { conclusion })];
  assert.deepEqual(
    keys(
      stopped({
        workflow: workflow(),
        runs: runs("startup_failure"),
        job,
        now,
      }),
    ),
    ["startup failure"],
  );
  assert.deepEqual(
    keys(
      stopped({
        workflow: workflow(),
        runs: runs("failure"),
        jobs: [{ name: job, conclusion: "failure" }],
        job,
        now,
      }),
    ),
    ["job failed"],
  );
  assert.match(
    stopped({ workflow: workflow(), runs: [run(2)], jobs: [], job, now })
      .problems[0].line,
    /Report to issues ended without running/,
  );
});

test("a step can be what had to succeed, when its job fails by design", () => {
  const step = "Check the scheduled scans";
  const jobs = (conclusion) => [
    {
      name: "Lane times",
      conclusion: "failure",
      steps: [{ name: step, conclusion }],
    },
  ];
  const check = (conclusion) =>
    keys(
      stopped({
        workflow: workflow(),
        runs: [run(2, { conclusion: "failure" })],
        jobs: jobs(conclusion),
        job: "Lane times",
        step,
        now,
      }),
    );
  assert.deepEqual(check("success"), []);
  assert.deepEqual(check("failure"), ["job failed"]);
});

test("runs still going, or cancelled by the concurrency group, are passed over", () => {
  const result = stopped({
    workflow: workflow(),
    runs: [
      run(1, { status: "in_progress", conclusion: null }),
      run(2, { conclusion: "cancelled" }),
      run(25),
    ],
    jobs: passed,
    job,
    now,
  });
  assert.deepEqual(keys(result), []);
  assert.equal(result.finished.id, 25);
});
