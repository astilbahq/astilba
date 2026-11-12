import assert from "node:assert/strict";
import { test } from "node:test";
import { stopped, windowMs } from "./schedule-watch.mjs";

const now = Date.parse("2026-11-13T13:00:00Z");
const run = (hoursAgo, fields = {}) => ({
  id: hoursAgo,
  created_at: new Date(now - hoursAgo * 3_600_000).toISOString(),
  html_url: `https://github.test/runs/${hoursAgo}`,
  status: "completed",
  conclusion: "success",
  ...fields,
});
const job = "Report to issues";
const keys = (result) => result.problems.map(({ key }) => key);

test("a workflow with no runs yet decides nothing", () => {
  assert.deepEqual(stopped({ runs: [], now }), {
    problems: [],
    undecided: true,
  });
});

test("a recent run whose report job succeeded is fine, however late GitHub started it", () => {
  const result = stopped({
    runs: [run(30)],
    jobs: [{ name: job, conclusion: "success" }],
    job,
    now,
  });
  assert.deepEqual(keys(result), []);
  assert.equal(result.undecided, false);
});

test("no run for a day and a half is a problem", () => {
  assert.deepEqual(
    keys(stopped({ runs: [run(windowMs / 3_600_000 + 1)], now })),
    ["no run"],
  );
});

test("a run that could not start, or whose report job failed, is a problem", () => {
  assert.deepEqual(
    keys(
      stopped({ runs: [run(2, { conclusion: "startup_failure" })], job, now }),
    ),
    ["startup failure"],
  );
  assert.deepEqual(
    keys(
      stopped({
        runs: [run(2, { conclusion: "failure" })],
        jobs: [{ name: job, conclusion: "failure" }],
        job,
        now,
      }),
    ),
    ["job failed"],
  );
  assert.match(
    stopped({ runs: [run(2)], jobs: [], job, now }).problems[0].line,
    /Report to issues ended without running/,
  );
});

test("a run still in progress is judged by the last one that finished", () => {
  const result = stopped({
    runs: [run(1, { status: "in_progress", conclusion: null }), run(25)],
    jobs: [{ name: job, conclusion: "success" }],
    job,
    now,
  });
  assert.deepEqual(keys(result), []);
  assert.equal(result.finished.id, 25);
});

test("without a job to check, a failed run that started is not a problem", () => {
  // The lane budget's own job fails while a lane is over budget, which it reports.
  assert.deepEqual(
    keys(stopped({ runs: [run(2, { conclusion: "failure" })], now })),
    [],
  );
});
