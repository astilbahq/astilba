import assert from "node:assert/strict";
import { test } from "node:test";
import {
  laneBudget,
  packageTimes,
  recentJobs,
  syncIssue,
} from "./lane-budget.mjs";

const integration = "github.com/astilbahq/astilba/services/platform/internal/integration";

test("reads what go test prints for a package, passed, failed or empty", () => {
  const log = [
    `2026-11-05T11:26:44.0911892Z ok  \t${integration}\t540.874s`,
    `2026-11-05T11:26:44.0911892Z FAIL\t${integration}\t907.495s`,
    `2026-11-05T11:26:44.0911892Z ok  \tgithub.com/example/empty\t1.012s [no tests to run]`,
    `ok  \tgithub.com/example/plain\t2.5s`,
    `2026-11-05T11:26:44.0911892Z ?   \tgithub.com/example/none\t[no test files]`,
    `2026-11-05T11:26:44.0911892Z FAIL\tgithub.com/example/broken [build failed]`,
    `2026-11-05T11:26:44.0911892Z --- PASS: TestPassed (540.00s)`,
    `2026-11-05T11:26:44.0911892Z FAIL`,
    `2026-11-05T11:26:44.0911892Z ok  \t@someone\t1.0s`,
    `2026-11-05T11:26:44.0911892Z ok  \t[link](https://example.test)\t1.0s`,
  ].join("\n");
  assert.deepEqual(packageTimes(log), [
    { name: integration, seconds: 540.874 },
    { name: integration, seconds: 907.495 },
    { name: "github.com/example/empty", seconds: 1.012 },
    { name: "github.com/example/plain", seconds: 2.5 },
  ]);
});

const job = (run, lane, seconds, conclusion = "success") => ({
  run,
  lane,
  conclusion,
  times: [{ name: "pkg", seconds }],
});

test("a lane's median must exceed the budget, so one slow runner does not fail it", () => {
  const jobs = [100, 650, 120].map((seconds, i) => job(i, "fast", seconds));
  assert.deepEqual(laneBudget(jobs, 600).problems, []);
  assert.equal(laneBudget(jobs, 600).rows[0].seconds, 120);
});

test("reports a lane whose median is past the budget, with its timed-out runs", () => {
  const { problems } = laneBudget(
    [
      job(1, "remaining", 610),
      job(2, "remaining", 907, "failure"),
      job(3, "remaining", 590),
      job(1, "preview", 280),
    ],
    600,
  );
  assert.equal(problems.length, 1);
  assert.match(problems[0], /remaining: `pkg` has a median of 610s over 3 runs/);
});

test("fewer than three runs of a lane decide nothing", () => {
  const few = laneBudget([job(1, "new", 900), job(2, "new", 900)], 600);
  assert.deepEqual(few.problems, []);
  assert.equal(few.undecided, true);
  const enough = laneBudget([1, 2, 3].map((run) => job(run, "new", 100)), 600);
  assert.equal(enough.undecided, false);
});

test("logs it cannot read fail rather than pass", () => {
  assert.match(
    laneBudget([{ run: 1, lane: "a", conclusion: "success", times: [] }])
      .problems[0],
    /run 1: a succeeded with no package times/,
  );
  assert.match(laneBudget([]).problems[0], /found no lane times/);
});

test("a failed job with no times is a build failure, not an unreadable log", () => {
  assert.deepEqual(
    laneBudget([
      { run: 1, lane: "a", conclusion: "failure", times: [] },
      job(2, "a", 10),
    ]).problems,
    [],
  );
});

const issueRequests = (issues) => {
  const sent = [];
  return {
    sent,
    request: async (path, { method = "GET", body } = {}) => {
      if (method !== "GET") sent.push({ method, path, ...body });
      // GitHub answers an unknown creator with an empty list, not an error.
      else assert.match(path, /[?&]creator=github-actions%5Bbot%5D(&|$)/);
      return { json: async () => issues };
    },
  };
};
const title = "Go race lanes are past their time budget";
const over = { problems: ["over"], undecided: false };

test("keeps one current issue open while over budget and closes it afterwards", async () => {
  const none = issueRequests([]);
  await syncIssue(none.request, "o/r", over, "body");
  assert.deepEqual(none.sent, [
    { method: "POST", path: "/repos/o/r/issues", title, body: "body" },
  ]);

  const open = [{ number: 7, title }];
  const still = issueRequests(open);
  await syncIssue(still.request, "o/r", over, "today");
  assert.deepEqual(still.sent, [
    { method: "PATCH", path: "/repos/o/r/issues/7", body: "today" },
  ]);

  const unsure = issueRequests(open);
  await syncIssue(unsure.request, "o/r", { problems: [], undecided: true }, "");
  assert.deepEqual(unsure.sent, []);

  const other = issueRequests([{ number: 8, title: "Another issue" }]);
  await syncIssue(other.request, "o/r", { problems: [], undecided: false }, "");
  assert.deepEqual(other.sent, []);

  const recovered = issueRequests(open);
  await syncIssue(
    recovered.request,
    "o/r",
    { problems: [], undecided: false },
    "",
  );
  assert.deepEqual(recovered.sent, [
    {
      method: "PATCH",
      path: "/repos/o/r/issues/7",
      state: "closed",
      state_reason: "completed",
    },
  ]);
});

test("samples recent runs of the current lanes in which every lane finished", async () => {
  const lanes = (...entries) => ({
    jobs: [
      { id: 0, name: "Application verification", conclusion: "success" },
      ...entries.map(([id, lane, conclusion = "success"]) => ({
        id,
        name: `Go race (${lane})`,
        conclusion,
      })),
    ],
  });
  const runs = new Map([
    [1, lanes([11, "a"], [12, "b"])],
    [2, { jobs: [{ id: 0, name: "Go race (${{ matrix.lane }})" }] }],
    [3, lanes([31, "a", "cancelled"], [32, "b"])],
    [4, lanes([41, "b", "failure"], [42, "a"])],
    [5, lanes([51, "a"])],
  ]);
  const jobs = await recentJobs(async (path) => {
    if (path.includes("/workflows/"))
      return {
        json: async () => ({ workflow_runs: [...runs.keys()].map((id) => ({ id })) }),
      };
    const run = /runs\/(\d+)\/jobs/.exec(path)?.[1];
    if (run) return { json: async () => runs.get(Number(run)) };
    return { text: async () => `ok  \tpkg\t${/jobs\/(\d+)/.exec(path)[1]}.0s` };
  }, "o/r");
  assert.deepEqual(
    jobs.map(({ run, lane, conclusion, times }) => [
      run,
      lane,
      conclusion,
      times[0].seconds,
    ]),
    [
      [1, "a", "success", 11],
      [1, "b", "success", 12],
      [4, "b", "failure", 41],
      [4, "a", "success", 42],
    ],
  );
});
