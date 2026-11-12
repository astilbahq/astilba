import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import {
  budgetSeconds,
  laneBudget,
  packageTimes,
  recentJobs,
} from "./lane-budget.mjs";

const integration =
  "github.com/astilbahq/astilba/services/platform/internal/integration";

test("the integration package warns at the same budget", () => {
  const fixture = readFileSync(
    "services/platform/internal/integration/fixture_test.go",
    "utf8",
  );
  assert.match(
    fixture,
    new RegExp(`^const laneBudget = ${budgetSeconds} \\* time\\.Second$`, "m"),
  );
});

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
  assert.match(
    problems[0].line,
    /remaining: `pkg` has a median of 610s over 3 runs/,
  );
  // Today's median is not part of the key, so the issue does not comment every day.
  assert.equal(problems[0].key, "over\tremaining\tpkg");
});

test("fewer than three runs of a lane decide nothing", () => {
  const few = laneBudget([job(1, "new", 900), job(2, "new", 900)], 600);
  assert.deepEqual(few.problems, []);
  assert.equal(few.undecided, true);
  const enough = laneBudget(
    [1, 2, 3].map((run) => job(run, "new", 100)),
    600,
  );
  assert.equal(enough.undecided, false);
});

test("logs it cannot read fail rather than pass", () => {
  assert.match(
    laneBudget([{ run: 1, lane: "a", conclusion: "success", times: [] }])
      .problems[0].line,
    /run 1: a succeeded with no package times/,
  );
  assert.match(laneBudget([]).problems[0].line, /found no lane times/);
  // Unread logs are a check that did not run, so the issue keeps its earlier problems.
  assert.ok(laneBudget([]).problems.every(({ check }) => check));
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
        json: async () => ({
          workflow_runs: [...runs.keys()].map((id) => ({ id })),
        }),
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
