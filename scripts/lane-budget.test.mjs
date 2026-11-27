import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import {
  budgetSeconds,
  commitBudgets,
  durationSeconds,
  familyMatcher,
  laneBudget,
  nameSplit,
  nativeBudgets,
  nativeLaneBudget,
  nativeLanes,
  nativeShare,
  packageTimes,
  recentJobs,
  splitReport,
  testTimes,
  timesTable,
} from "./lane-budget.mjs";

const integration =
  "github.com/astilbahq/astilba/services/platform/internal/integration";

test("the test packages warn at the same budgets", () => {
  const warning = readFileSync(
    "services/platform/internal/lanebudget/lanebudget.go",
    "utf8",
  );
  assert.match(
    warning,
    new RegExp(`^const RaceBudget = ${budgetSeconds} \\* time\\.Second$`, "m"),
  );
  assert.ok(warning.includes(`\nconst NativeShare = ${nativeShare}\n`));
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

test("takes the current lanes from the newest run whose lanes all finished", async () => {
  const lanes = (...entries) => ({
    jobs: entries.map(([id, lane, conclusion = "success"]) => ({
      id,
      name: `Go race (${lane})`,
      conclusion,
    })),
  });
  // Runs 1 and 2 added lane c, and neither finished it, so the newest complete lanes
  // are still a and b. Run 5 ran c too, but before a rebalancing that took it away.
  const runs = new Map([
    [1, lanes([11, "a", "cancelled"], [12, "b"], [13, "c"])],
    [2, lanes([21, "a"], [22, "b"], [23, "c", "skipped"])],
    [3, lanes([31, "a"], [32, "b"])],
    [4, lanes([41, "a"], [42, "b", "failure"])],
    [5, lanes([51, "a"], [52, "b"], [53, "c"])],
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
    jobs.map(({ run, lane }) => [run, lane]),
    [
      [3, "a"],
      [3, "b"],
      [4, "a"],
      [4, "b"],
    ],
  );
});

test("gives each top-level test the package whose result follows it", () => {
  const log = [
    "2026-11-05T11:20:00.0000000Z === RUN   TestFirst",
    "2026-11-05T11:20:00.0000000Z --- PASS: TestFirst (12.50s)",
    "2026-11-05T11:20:00.0000000Z     --- PASS: TestFirst/part (12.00s)",
    "2026-11-05T11:20:00.0000000Z --- FAIL: TestSecond (3.00s)",
    "2026-11-05T11:20:00.0000000Z --- SKIP: TestThird (0.00s)",
    `2026-11-05T11:20:00.0000000Z FAIL\t${integration}\t20.000s`,
    "--- PASS: TestBuilt (1.00s)",
    "2026-11-05T11:20:00.0000000Z FAIL\tgithub.com/example/broken [build failed]",
    "--- PASS: TestUntimed (1.00s)",
    "?   \tgithub.com/example/none\t[no test files]",
    "--- PASS: TestPlain (2.00s)",
    "ok  \tgithub.com/example/plain\t2.5s",
    "--- PASS: TestNoPackage (9.00s)",
  ].join("\n");
  assert.deepEqual(testTimes(log), [
    { package: integration, name: "TestFirst", seconds: 12.5 },
    { package: integration, name: "TestSecond", seconds: 3 },
    { package: integration, name: "TestThird", seconds: 0 },
    { package: "github.com/example/plain", name: "TestPlain", seconds: 2 },
  ]);
});

test("reads the family lanes from the Makefile the lanes run", () => {
  const isFamily = familyMatcher(readFileSync("Makefile", "utf8"));
  assert.equal(isFamily("TestCrossHostCoordinatedPlacement"), true);
  assert.equal(isFamily("TestGitImportKeepsItsBranch"), true);
  assert.equal(isFamily("TestCrossHostCoordinatedPlacementAgain"), false);
  assert.equal(isFamily("TestOrdinary"), false);
  assert.throws(
    () => familyMatcher("APP_OTHER = ^TestA\n"),
    /APP_FAMILY_TESTS/,
  );
});

// One job of a name lane: its integration package took the tests' time plus overhead.
const lane = (run, tests, overhead = 10, name = integration) => ({
  run,
  lane: "any",
  conclusion: "success",
  times: [
    {
      name,
      seconds: overhead + tests.reduce((sum, [, seconds]) => sum + seconds, 0),
    },
  ],
  tests: tests.map(([test, seconds]) => ({
    package: name,
    name: test,
    seconds,
  })),
});
const noFamily = () => false;

test("proposes the fewest first-letter lanes within the target, from median times", () => {
  const tests = [
    ["TestAlpha", 100],
    ["TestBeta", 100],
    ["TestDelta", 150],
    ["TestEcho", 150],
    ["TestZulu", 50],
  ];
  const jobs = [1, 2, 3].map((run) =>
    lane(
      run,
      // One slow run of each test does not move its median.
      tests.map(([name, seconds]) => [name, run === 2 ? seconds * 3 : seconds]),
    ),
  );
  const split = nameSplit(jobs, noFamily, 300);
  assert.equal(split.fits, true);
  assert.deepEqual(
    split.lanes.map(({ lane, from, to, seconds }) => [lane, from, to, seconds]),
    [
      // C has no tests, so it joins the range after it.
      ["a-to-b", "A", "B", 210],
      ["c-to-d", "C", "D", 160],
      ["e-to-z", "E", "Z", 210],
    ],
  );
  assert.equal(split.lanes[0].package, integration);
});

test("a test a family lane selects, or a family lane's package, is not split", () => {
  const jobs = [1, 2, 3].flatMap((run) => [
    lane(run, [
      ["TestAlpha", 100],
      ["TestBeta", 100],
    ]),
    lane(run, [
      ["TestGitOne", 500],
      ["TestOther", 500],
    ]),
  ]);
  const split = nameSplit(jobs, (name) => name.startsWith("TestGit"), 300);
  assert.deepEqual(
    split.lanes.map(({ lane, seconds }) => [lane, seconds]),
    [["a-to-z", 210]],
  );
});

test("a name outside A to Z goes with the last lane, as the last lane runs it", () => {
  const jobs = [1, 2, 3].map((run) =>
    lane(run, [
      ["TestAlpha", 100],
      ["TestZulu", 100],
      ["Test_odd", 100],
    ]),
  );
  assert.deepEqual(
    nameSplit(jobs, noFamily, 220).lanes.map(({ lane, seconds }) => [
      lane,
      seconds,
    ]),
    [
      ["a", 110],
      ["b-to-z", 210],
    ],
  );
});

test("each package is projected alone, with its own overhead", () => {
  const other = "github.com/example/other";
  const jobs = [1, 2, 3].flatMap((run) => [
    lane(run, [
      ["TestAlpha", 100],
      ["TestMike", 100],
    ]),
    lane(run, [["TestAlpha", 150]], 50, other),
  ]);
  const { lanes } = nameSplit(jobs, noFamily, 210);
  assert.deepEqual(
    lanes.map(({ lane, seconds, package: name }) => [lane, seconds, name]),
    [["a-to-z", 210, integration]],
  );
  assert.deepEqual(
    nameSplit(jobs, noFamily, 199).lanes.map(
      ({ lane, seconds, package: name }) => [lane, seconds, name],
    ),
    [
      ["a", 200, other],
      ["b-to-z", 110, integration],
    ],
  );
});

test("when one letter alone is past the target, reports it and the fewest closest lanes", () => {
  const jobs = [1, 2, 3].map((run) =>
    lane(run, [
      ["TestAlpha", 100],
      ["TestBravo", 100],
      ["TestCharlie", 100],
      ["TestRecoveryOne", 300],
      ["TestRecoveryTwo", 300],
    ]),
  );
  const split = nameSplit(jobs, noFamily, 400);
  assert.equal(split.fits, false);
  // Four lanes would come no closer than two.
  assert.deepEqual(
    split.lanes.map(({ lane, seconds }) => [lane, seconds]),
    [
      ["a-to-c", 310],
      ["d-to-z", 610],
    ],
  );
  assert.match(
    splitReport(split, 400),
    /^No split by first letter keeps every package within 400s: the `TestR` tests alone are projected at 610s\./,
  );
});

test("reports the split as a table, and nothing without test times", () => {
  const split = nameSplit(
    [1, 2, 3].map((run) => lane(run, [["TestAlpha", 100]])),
    noFamily,
  );
  assert.equal(
    splitReport(split, 400),
    [
      "The fewest name lanes that keep every package within 400s, from the same runs' test times:",
      "",
      "| Lane | Names | Slowest package | Projected seconds |",
      "| --- | --- | --- | --- |",
      `| a-to-z | \`TestA\` to \`TestZ\` | \`${integration}\` | 110 |`,
    ].join("\n"),
  );
  assert.equal(nameSplit([], noFamily), undefined);
  assert.equal(splitReport(undefined), "");
});

test("reads Go durations, and refuses what it cannot", () => {
  assert.equal(durationSeconds("30m"), 1800);
  assert.equal(durationSeconds("1h30m"), 5400);
  assert.equal(durationSeconds("90s"), 90);
  assert.equal(durationSeconds("1.5h"), 5400);
  assert.equal(durationSeconds("1500ms"), 1.5);
  for (const text of ["", "30", "30x", "-5m", "m"])
    assert.throws(() => durationSeconds(text), /cannot read the duration/);
});

const nativeMakefile = [
  "GO = go test -race -count=1 -v",
  "verify-agent-host:",
  "\t$(MAKE) verify-agent-host-a",
  "\t$(MAKE) verify-agent-host-b",
  "verify-agent-host-a:",
  "\tcd services/platform && $(GO) -timeout=30m -run '^TestNativeA$$' ./internal/agent",
  "\tcd services/platform && $(GO) -timeout=15m -timeout=10m -run '^TestNativeB$$' ./internal/integration",
  "verify-agent-host-b:",
  "\tcd services/platform && $(GO) -run '^TestNativeC$$' ./internal/agent",
  "verify-other:",
  "\tcd services/platform && $(GO) -timeout=1m ./internal/other",
  "",
].join("\n");

test("allows each native package a share of its own command's timeout", () => {
  assert.deepEqual(
    nativeBudgets(nativeMakefile),
    new Map([
      [
        "a",
        new Map([
          ["services/platform/internal/agent", 1080],
          ["services/platform/internal/integration", 360],
        ]),
      ],
      // Go's own default of 10 minutes, as go test applies the last -timeout above.
      ["b", new Map([["services/platform/internal/agent", 360]])],
    ]),
  );
});

test("refuses a native package whose time it could not attribute to one command", () => {
  assert.throws(
    () =>
      nativeBudgets(
        nativeMakefile.replace("./internal/integration", "./internal/agent"),
      ),
    /verify-agent-host-a runs services\/platform\/internal\/agent in more than one go test command/,
  );
  assert.throws(
    () =>
      nativeBudgets(
        nativeMakefile.replace("./internal/integration", "./internal/..."),
      ),
    /cannot list/,
  );
});

test("every native lane in the Makefile has a budget for each package it runs", () => {
  const budgets = nativeBudgets(readFileSync("Makefile", "utf8"));
  assert.ok(budgets.has("core"));
  for (const [lane, packages] of budgets) {
    assert.ok(packages.size, `${lane} runs no package`);
    for (const seconds of packages.values()) assert.ok(seconds > 0);
  }
});

const agent = "github.com/astilbahq/astilba/services/platform/internal/agent";
const integrationNative =
  "github.com/astilbahq/astilba/services/platform/internal/integration";
const host = "github.com/astilbahq/astilba/services/platform/host";
const nativeJob = (run, times, conclusion = "success", target = "a") => ({
  run,
  lane: `${target} (arm64)`,
  target,
  conclusion,
  times: Object.entries(times).map(([name, seconds]) => ({ name, seconds })),
});

test("holds each native package to its own budget and leaves out the bootstrap's", () => {
  const budgets = () => nativeBudgets(nativeMakefile);
  const { problems, rows } = nativeLaneBudget(
    [1, 2, 3].map((run) =>
      nativeJob(run, { [agent]: 1100, [integrationNative]: 300, [host]: 9000 }),
    ),
    budgets,
  );
  assert.deepEqual(
    rows.map(({ lane, name, seconds, budget }) => [
      lane,
      name,
      seconds,
      budget,
    ]),
    [
      ["a (arm64)", agent, 1100, 1080],
      ["a (arm64)", integrationNative, 300, 360],
    ],
  );
  assert.deepEqual(
    problems.map(({ key }) => key),
    [`over\ta (arm64)\t${agent}`],
  );
  assert.match(problems[0].line, /past the 1080s budget/);
});

test("a native lane without a time for one of its packages fails rather than passes", () => {
  const budgets = () => nativeBudgets(nativeMakefile);
  const missing = nativeLaneBudget(
    [nativeJob(1, { [agent]: 100, [host]: 60 })],
    budgets,
  ).problems;
  assert.deepEqual(missing, [
    {
      key: "untimed\ta (arm64)\tservices/platform/internal/integration",
      line: "run 1: a (arm64) succeeded with no time for services/platform/internal/integration",
      check: true,
    },
  ]);
  // A failed job may have stopped before a package, and one with no time at all is
  // reported once, as any lane's is.
  assert.deepEqual(
    nativeLaneBudget([nativeJob(1, { [agent]: 100 }, "failure")], budgets)
      .problems,
    [],
  );
  assert.deepEqual(
    nativeLaneBudget([nativeJob(1, { [host]: 60 })], budgets).problems.map(
      ({ line }) => line,
    ),
    [
      "run 1: a (arm64) succeeded with no package times",
      "found no lane times to compare",
    ],
  );
  assert.deepEqual(
    nativeLaneBudget(
      [1, 2].map((run) => nativeJob(run, { [agent]: 1 }, "success", "gone")),
      budgets,
    ).problems.filter(({ key }) => key.startsWith("unknown")),
    [
      {
        key: "unknown\tgone",
        line: "run 1: gone (arm64) has no verify-agent-host-gone in the Makefile",
        check: true,
      },
    ],
  );
});

test("names a native lane by its target and architecture", async () => {
  assert.deepEqual(nativeLanes.lane("Host (arm64, core)"), {
    lane: "core (arm64)",
    target: "core",
  });
  assert.equal(
    nativeLanes.lane("Host (${{ matrix.platform.arch }}, ${{ matrix.lane }})"),
    undefined,
  );
  assert.equal(nativeLanes.lane("Native verification"), undefined);
  const paths = [];
  const jobs = await recentJobs(
    async (path) => {
      paths.push(path);
      if (path.includes("/workflows/"))
        return { json: async () => ({ workflow_runs: [{ id: 1 }] }) };
      if (path.includes("/runs/"))
        return {
          json: async () => ({
            jobs: [
              { id: 7, name: "Host (amd64, core)", conclusion: "success" },
              { id: 0, name: "Native verification", conclusion: "success" },
            ],
          }),
        };
      return { text: async () => `ok  \t${agent}\t12.0s` };
    },
    "o/r",
    nativeLanes,
  );
  assert.match(paths[0], /\/workflows\/ci\.yml\/runs\?branch=main&event=push/);
  assert.deepEqual(
    jobs.map(({ lane, target, times }) => [lane, target, times[0].seconds]),
    [["core (amd64)", "core", 12]],
  );
});

test("shows a budget column only where budgets differ", () => {
  const rows = [
    { lane: "a", name: "pkg", seconds: 12, budget: 600, samples: 3 },
    { lane: "a", name: "quick", seconds: 2, budget: 600, samples: 3 },
  ];
  assert.equal(
    timesTable(rows),
    [
      "| Lane | Package | Median seconds | Runs |",
      "| --- | --- | --- | --- |",
      "| a | `pkg` | 12 | 3 |",
    ].join("\n"),
  );
  assert.equal(
    timesTable(rows, true).split("\n")[2],
    "| a | `pkg` | 12 | 600 | 3 |",
  );
});

test("judges each native job by the Makefile of the commit it ran", () => {
  // The newer commit renamed lane b to c and gave a 20-minute timeout to its package.
  const renamed = nativeMakefile
    .replaceAll("verify-agent-host-b", "verify-agent-host-c")
    .replace("$(GO) -run", () => "$(GO) -timeout=20m -run");
  const byCommit = new Map([
    ["new", nativeBudgets(renamed)],
    ["old", nativeBudgets(nativeMakefile)],
  ]);
  const jobs = [
    { ...nativeJob(3, { [agent]: 500 }, "success", "c"), commit: "new" },
    { ...nativeJob(2, { [agent]: 500 }, "success", "b"), commit: "old" },
    {
      ...nativeJob(1, { [agent]: 500, [integrationNative]: 10 }),
      commit: "old",
    },
  ];
  const { problems, rows } = nativeLaneBudget(jobs, (job) =>
    byCommit.get(job.commit),
  );
  // b's run predates the rename, and is past b's own budget then, but has one sample.
  assert.deepEqual(problems, []);
  assert.deepEqual(
    rows.map(({ lane, budget }) => [lane, budget]),
    [
      ["c (arm64)", 720],
      ["b (arm64)", 360],
      ["a (arm64)", 1080],
      ["a (arm64)", 360],
    ],
  );
});

test("a native job whose Makefile could not be read is a check that did not run", () => {
  const { problems } = nativeLaneBudget(
    [1, 2].map((run) => ({ ...nativeJob(run, { [agent]: 1 }), commit: "x" })),
    () => {
      throw new Error("GET Makefile returned 404");
    },
  );
  assert.deepEqual(
    problems.filter(({ key }) => key.startsWith("makefile")),
    [1, 2].map((run) => ({
      key: `makefile\t${run}`,
      line: `run ${run}: cannot read its Makefile: GET Makefile returned 404`,
      check: true,
    })),
  );
});

test("reads each sampled commit's Makefile once", async () => {
  const paths = [];
  const budgetsOf = await commitBudgets(
    async (path) => {
      paths.push(path);
      if (path.endsWith("ref=bad")) throw new Error("returned 404");
      return {
        json: async () => ({
          encoding: "base64",
          content: Buffer.from(nativeMakefile).toString("base64"),
        }),
      };
    },
    "o/r",
    [{ commit: "good" }, { commit: "good" }, { commit: "bad" }],
  );
  assert.deepEqual(paths, [
    "/repos/o/r/contents/Makefile?ref=good",
    "/repos/o/r/contents/Makefile?ref=bad",
  ]);
  assert.deepEqual(
    budgetsOf({ commit: "good" }),
    nativeBudgets(nativeMakefile),
  );
  assert.throws(() => budgetsOf({ commit: "bad" }), /returned 404/);
});
