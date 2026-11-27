import { appendFileSync, readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { compile, goTests, parseMakefile } from "./native-lanes.mjs";
import { github, syncIssue } from "./report-issue.mjs";

// Each lane's `go test -timeout` allows 900 seconds. One commit's package time has
// differed by 28% between hosted runners, so a lane is moved well before a slow
// runner can reach the limit. A failure here cannot eject a pull request, because
// only the Lane budget workflow reads these times.
export const budgetSeconds = 600;
// A split is proposed so each package is projected this far under the budget, which a
// runner 28% slower still meets, with room for the tests that land before the next one.
export const targetSeconds = 400;
const sampledRuns = 5;
const minimumRuns = 3;
const issueTitle = "Go race lanes are past their time budget";
// Native lanes set a -timeout for each go test command, from 5 to 30 minutes, so each
// package is held to this share of its own command's. A runner 28% slower still meets
// it, with room for the tests that land before the lanes are rebalanced.
export const nativeShare = 0.6;
const nativeIssueTitle = "Native lanes are past their time budget";
const nativePrefix = "verify-agent-host-";

// A name is limited to import path characters because it reaches a public issue, and
// test output could print a line of this shape with a mention or markup in it.
const packageTime =
  /^(?:\S+Z )?(?:ok|FAIL)\s+([\w./-]+)\s+([\d.]+)s(?: \[no tests to run\])?\s*$/gm;

// The package times `go test` printed in one lane's job log.
export function packageTimes(log) {
  return [...log.matchAll(packageTime)].map(([, name, seconds]) => ({
    name,
    seconds: Number(seconds),
  }));
}

const testTime =
  /^(?:\S+Z )?--- (?:PASS|FAIL|SKIP): (Test\w*) \(([\d.]+)s\)\s*$/;
const packageEnd = /^(?:\S+Z )?(?:ok|FAIL|\?)\s+\S/;

// Each top-level test's time, with its package. `go test` prints a package's output
// together and ends it with the package's result, so a test belongs to the next result
// line. A subtest is indented, and its parent's time includes it.
export function testTimes(log) {
  const found = [];
  let pending = [];
  for (const line of log.split("\n")) {
    const test = testTime.exec(line);
    if (test) pending.push({ name: test[1], seconds: Number(test[2]) });
    else if (packageEnd.test(line)) {
      const [time] = packageTimes(line);
      if (time)
        found.push(
          ...pending.map((entry) => ({ package: time.name, ...entry })),
        );
      pending = [];
    }
  }
  return found;
}

function median(values) {
  const sorted = values.toSorted((a, b) => a - b);
  const middle = sorted.length >> 1;
  return sorted.length % 2
    ? sorted[middle]
    : (sorted[middle - 1] + sorted[middle]) / 2;
}

// Takes each job a lane ran, with what its log held. A median of at least three runs
// ignores one slow runner, and fewer leaves a lane undecided. A successful job without
// a time means this parser no longer reads the logs, so it must not read as a pass.
// Each problem's key names what is wrong without today's numbers, so the issue says
// only what is new. Logs that could not be read are a check that did not run, which
// keeps the issue's earlier problems. `budget` is either one budget for every package or
// a function of a lane and package giving each its own.
export function laneBudget(jobs, budget = budgetSeconds) {
  const limit = typeof budget === "function" ? budget : () => budget;
  const problems = [];
  const problem = (key, line, check) =>
    problems.push({ key, line, ...(check && { check }) });
  const samples = new Map();
  for (const { run, lane, conclusion, times } of jobs) {
    if (conclusion === "success" && !times.length)
      problem(
        `untimed\t${lane}`,
        `run ${run}: ${lane} succeeded with no package times`,
        true,
      );
    for (const { name, seconds } of times) {
      const key = `${lane}\t${name}`;
      samples.set(key, [...(samples.get(key) ?? []), seconds]);
    }
  }
  if (!samples.size)
    problem("no times", "found no lane times to compare", true);
  const rows = [...samples]
    .map(([key, seconds]) => {
      const [lane, name] = key.split("\t");
      return {
        lane,
        name,
        seconds: Math.round(median(seconds)),
        samples: seconds.length,
        budget: limit(lane, name),
      };
    })
    .toSorted((a, b) => b.seconds - a.seconds);
  for (const { lane, name, seconds, samples: count, budget: allowed } of rows)
    if (count >= minimumRuns && seconds > allowed)
      problem(
        `over\t${lane}\t${name}`,
        `${lane}: \`${name}\` has a median of ${seconds}s over ${count} runs, past the ${allowed}s budget. Rebalance the lanes, as docs/continuous-integration.md describes.`,
      );
  const undecided = rows.some(({ samples: count }) => count < minimumRuns);
  return { problems, undecided, rows };
}

const durationUnits = { h: 3600, m: 60, s: 1, ms: 0.001 };

// Seconds in a Go duration such as 30m or 1h30m.
export function durationSeconds(text) {
  if (!/^(?:\d+(?:\.\d+)?(?:ms|h|m|s))+$/.test(text))
    throw new Error(`cannot read the duration ${text}`);
  return [...text.matchAll(/(\d+(?:\.\d+)?)(ms|h|m|s)/g)].reduce(
    (sum, [, amount, unit]) => sum + Number(amount) * durationUnits[unit],
    0,
  );
}

// Each native lane's packages, with the seconds each is allowed: a share of the timeout of
// the go test command that runs it. go test prints one time for a package, so a lane that
// ran a package in two commands could not say which command took it.
export function nativeBudgets(makefile, share = nativeShare) {
  const { recipes, expand } = parseMakefile(makefile);
  const lanes = new Map();
  for (const [target, lines] of recipes) {
    if (!target.startsWith(nativePrefix)) continue;
    const packages = new Map();
    for (const command of lines.flatMap((line) => goTests(expand(line))))
      for (const directory of command.packages) {
        if (directory.endsWith("/..."))
          throw new Error(
            `${target} runs ${directory}, whose packages this check cannot list`,
          );
        if (packages.has(directory))
          throw new Error(
            `${target} runs ${directory} in more than one go test command, so its time cannot be told apart`,
          );
        packages.set(
          directory,
          Math.round(durationSeconds(command.timeout) * share),
        );
      }
    lanes.set(target.slice(nativePrefix.length), packages);
  }
  return lanes;
}

// go test prints a package's import path, which ends with its directory.
const inDirectory = (name, directory) =>
  name === directory || name.endsWith(`/${directory}`);

// Native jobs, timed by their lane's Makefile commands. Each job is judged by the Makefile
// of the commit it ran, as its packages and timeouts were then: a run from before the
// lanes changed is neither stale nor missing a package added since. A row's budget is
// the newest run's. Each job also runs packages outside its lane's target, such as the
// bootstrap's, and those have their own limits in the workflow, so only the target's
// packages are compared. A successful job without a time for one of them means this
// check no longer reads it, so it must not read as a pass. `budgetsOf` gives a job's
// nativeBudgets, newest jobs first.
export function nativeLaneBudget(jobs, budgetsOf) {
  // Every run repeats these, and the issue lists each once.
  const problems = new Map();
  const problem = (key, line) =>
    problems.has(key) || problems.set(key, { key, line, check: true });
  const allowed = new Map();
  const timed = [];
  for (const job of jobs) {
    let budgets;
    try {
      budgets = budgetsOf(job);
    } catch (error) {
      problem(
        `makefile\t${job.run}`,
        `run ${job.run}: cannot read its Makefile: ${error.message}`,
      );
      continue;
    }
    const packages = budgets.get(job.target);
    if (!packages) {
      problem(
        `unknown\t${job.target}`,
        `run ${job.run}: ${job.lane} has no ${nativePrefix}${job.target} in the Makefile`,
      );
      continue;
    }
    const times = [];
    const missing = [];
    for (const [directory, seconds] of packages) {
      const found = job.times.filter(({ name }) =>
        inDirectory(name, directory),
      );
      for (const { name } of found) {
        const key = `${job.lane}\t${name}`;
        if (!allowed.has(key)) allowed.set(key, seconds);
      }
      times.push(...found);
      if (!found.length) missing.push(directory);
    }
    // A job with no time at all is reported below, as any lane's would be.
    if (job.conclusion === "success" && times.length)
      for (const directory of missing)
        problem(
          `untimed\t${job.lane}\t${directory}`,
          `run ${job.run}: ${job.lane} succeeded with no time for ${directory}`,
        );
    timed.push({ ...job, times });
  }
  const result = laneBudget(timed, (lane, name) =>
    allowed.get(`${lane}\t${name}`),
  );
  return { ...result, problems: [...problems.values(), ...result.problems] };
}

// Which tests the family lanes select, read from the Makefile the lanes run.
export function familyMatcher(makefile) {
  const pattern = parseMakefile(makefile).expand("$(APP_FAMILY_TESTS)");
  if (pattern.includes("\0"))
    throw new Error("the Makefile does not define APP_FAMILY_TESTS");
  const branches = compile(pattern);
  return (name) => branches.some(({ expression }) => expression.test(name));
}

const letters = [..."ABCDEFGHIJKLMNOPQRSTUVWXYZ"];
const total = (values) => values.reduce((sum, value) => sum + value, 0);

// Splits the tests no family selects into the fewest ranges of the letter after `Test`
// that keep every package's projected time within the target. A projection adds the
// medians of a range's tests in a package to that package's median time outside its
// tests, such as its fixture's start. A name outside A to Z goes with the last range,
// as the last name lane takes whatever the others leave. When no split fits, because a
// letter alone is past the target, this returns the fewest lanes that come closest and
// the slowest letter.
export function nameSplit(jobs, isFamily, target = targetSeconds) {
  const samples = new Map();
  const overheads = new Map();
  const add = (map, key, value) =>
    map.set(key, [...(map.get(key) ?? []), value]);
  for (const { times, tests } of jobs) {
    for (const { name, seconds } of times) {
      const ran = tests.filter((test) => test.package === name);
      if (!ran.length || ran.some((test) => isFamily(test.name))) continue;
      add(overheads, name, seconds - total(ran.map((test) => test.seconds)));
      for (const test of ran)
        add(samples, `${name}\t${test.name}`, test.seconds);
    }
  }
  if (!samples.size) return;

  // Seconds per package for each letter; index 26 holds names outside A to Z.
  const packages = new Map();
  for (const [key, seconds] of samples) {
    const [name, test] = key.split("\t");
    const index = letters.indexOf(test[4]);
    const row = packages.get(name) ?? Array(letters.length + 1).fill(0);
    row[index < 0 ? letters.length : index] += median(seconds);
    packages.set(name, row);
  }
  const cost = (from, to) => {
    let worst = { seconds: 0 };
    for (const [name, row] of packages) {
      const end = to === letters.length - 1 ? row.length : to + 1;
      const tests = total(row.slice(from, end));
      if (!tests) continue;
      const seconds = tests + Math.max(0, median(overheads.get(name)));
      if (seconds > worst.seconds) worst = { seconds, package: name };
    }
    return worst;
  };
  // A range ends at a letter with tests, or at Z, so a letter without tests joins the
  // range after it and no lane is left with nothing to run.
  const rows = [...packages.values()];
  const starts = letters
    .map((_, index) => index)
    .filter(
      (index) =>
        index === 0 ||
        (rows.some((row) => row[index - 1] > 0) &&
          rows.some((row) => row.slice(index).some((seconds) => seconds > 0))),
    );
  const ranges = (count) => {
    // best[i][k]: the lowest worst cost splitting starts[i..] into k ranges.
    const memo = new Map();
    const best = (i, k) => {
      const key = `${i},${k}`;
      if (memo.has(key)) return memo.get(key);
      let result;
      if (k === 1)
        result = {
          seconds: cost(starts[i], letters.length - 1).seconds,
          cuts: [],
        };
      else
        for (let j = i + 1; starts.length - j >= k - 1; j++) {
          const rest = best(j, k - 1);
          const seconds = Math.max(
            cost(starts[i], starts[j] - 1).seconds,
            rest.seconds,
          );
          if (!result || seconds < result.seconds)
            result = { seconds, cuts: [j, ...rest.cuts] };
        }
      memo.set(key, result);
      return result;
    };
    const bounds = [0, ...best(0, count).cuts, starts.length];
    return bounds.slice(0, -1).map((bound, i) => {
      const from = starts[bound];
      const to =
        bounds[i + 1] === starts.length
          ? letters.length - 1
          : starts[bounds[i + 1]] - 1;
      const lane = (
        from === to ? letters[from] : `${letters[from]}-to-${letters[to]}`
      ).toLowerCase();
      return { lane, from: letters[from], to: letters[to], ...cost(from, to) };
    });
  };
  let closest;
  for (let count = 1; count <= starts.length; count++) {
    const lanes = ranges(count);
    const seconds = Math.max(...lanes.map((lane) => lane.seconds));
    if (seconds <= target) return { fits: true, lanes };
    if (!closest || seconds < closest.seconds) closest = { seconds, lanes };
  }
  const slowest = letters
    .map((letter, index) => ({ letter, ...cost(index, index) }))
    .reduce((a, b) => (b.seconds > a.seconds ? b : a));
  return { fits: false, lanes: closest.lanes, slowest };
}

// The proposed split as Markdown, for the job summary and the issue.
export function splitReport(split, target = targetSeconds) {
  if (!split) return "";
  const { fits, lanes, slowest } = split;
  return [
    fits
      ? `The fewest name lanes that keep every package within ${target}s, from the same runs' test times:`
      : `No split by first letter keeps every package within ${target}s: the \`Test${slowest.letter}\` tests alone are projected at ${Math.round(slowest.seconds)}s. Move a family of them to a lane of its own. The fewest lanes that come closest:`,
    "",
    "| Lane | Names | Slowest package | Projected seconds |",
    "| --- | --- | --- | --- |",
    ...lanes.map(
      ({ lane, from, to, package: name, seconds }) =>
        `| ${lane} | \`Test${from}\` to \`Test${to}\` | \`${name}\` | ${Math.round(seconds)} |`,
    ),
  ].join("\n");
}

// Where each kind of lane runs, and the lane a job's name gives. A native lane runs once
// for each architecture, whose runners differ in speed, so each is a lane of its own here.
export const raceLanes = {
  workflow: "application.yml",
  lane: (name) => {
    const lane = /^Go race \(([\w-]+)\)$/.exec(name)?.[1];
    return lane && { lane };
  },
};
export const nativeLanes = {
  workflow: "ci.yml",
  lane: (name) => {
    const [, architecture, target] =
      /^Host \(([\w-]+), ([\w-]+)\)$/.exec(name) ?? [];
    return target && { lane: `${target} (${architecture})`, target };
  },
};

// Main pushes run every lane once, and a newer run cannot cancel them. A lane that did
// not finish, such as one stopped at the job's time limit, has no time, so its run is
// passed over, and today's lanes are those of the newest run whose lanes all finished.
// A run that ran other lanes predates a rebalancing, so its times would misreport
// today's lanes. A run that selected no native checks has no native lanes, so it is
// passed over.
export async function recentJobs(request, repository, lanesOf = raceLanes) {
  const { workflow_runs: runs } = await (
    await request(
      `/repos/${repository}/actions/workflows/${lanesOf.workflow}/runs?branch=main&event=push&status=completed&per_page=${sampledRuns * 3}`,
    )
  ).json();
  let sampled = 0;
  let current;
  const jobs = [];
  for (const { id, head_sha: commit } of runs) {
    if (sampled === sampledRuns) break;
    const { jobs: listed } = await (
      await request(`/repos/${repository}/actions/runs/${id}/jobs?per_page=100`)
    ).json();
    const lanes = listed.flatMap((job) => {
      const lane = lanesOf.lane(job.name);
      return lane ? [{ ...job, ...lane }] : [];
    });
    if (!lanes.length) continue;
    const names = lanes
      .map(({ lane }) => lane)
      .sort()
      .join();
    if (
      !lanes.every(({ conclusion }) =>
        ["success", "failure"].includes(conclusion),
      )
    )
      continue;
    current ??= names;
    if (names !== current) continue;
    sampled++;
    for (const { id: job, lane, target, conclusion } of lanes) {
      const log = await (
        await request(`/repos/${repository}/actions/jobs/${job}/logs`)
      ).text();
      jobs.push({
        run: id,
        commit,
        lane,
        ...(target && { target }),
        conclusion,
        times: packageTimes(log),
        tests: testTimes(log),
      });
    }
  }
  return jobs;
}

// Reads the Makefile each sampled commit ran, once a commit. What one could not be read
// for throws when a job of that commit asks, so only its jobs go unjudged.
export async function commitBudgets(request, repository, jobs) {
  const budgets = new Map();
  for (const commit of new Set(jobs.map((job) => job.commit)))
    try {
      const { content, encoding } = await (
        await request(
          `/repos/${repository}/contents/Makefile?ref=${encodeURIComponent(commit)}`,
        )
      ).json();
      if (encoding !== "base64")
        throw new Error(`the Makefile came as ${encoding}`);
      budgets.set(
        commit,
        nativeBudgets(Buffer.from(content, "base64").toString("utf8")),
      );
    } catch (error) {
      budgets.set(commit, error);
    }
  return (job) => {
    const found = budgets.get(job.commit);
    if (found instanceof Error) throw found;
    return found;
  };
}

// The median times as Markdown, leaving out packages too quick to matter.
export function timesTable(rows, budgets = false) {
  return [
    `| Lane | Package | Median seconds |${budgets ? " Budget |" : ""} Runs |`,
    `| --- | --- | --- |${budgets ? " --- |" : ""} --- |`,
    ...rows
      .filter(({ seconds }) => seconds >= 10)
      .map(
        ({ lane, name, seconds, budget, samples }) =>
          `| ${lane} | \`${name}\` | ${seconds} |${budgets ? ` ${budget} |` : ""} ${samples} |`,
      ),
  ].join("\n");
}

// Shows one kind of lane's times and, in the workflow, keeps its issue.
async function report(
  request,
  repository,
  { heading, title, problems, undecided, table },
) {
  console.log(`${heading}\n\n${table}\n`);
  // Only the workflow reports, so a local run changes nothing.
  if (!process.env.GITHUB_RUN_ID) return;
  appendFileSync(
    process.env.GITHUB_STEP_SUMMARY,
    `## ${heading}\n\n${table}\n\n`,
  );
  await syncIssue(request, repository, {
    title,
    problems,
    undecided,
    body: [
      ...(problems.length
        ? problems.map(({ line }) => line)
        : ["Every lane is within budget again."]),
      "",
      table,
      "",
      `${process.env.GITHUB_SERVER_URL}/${repository}/actions/runs/${process.env.GITHUB_RUN_ID}`,
      "",
      "See the lane budget in `docs/continuous-integration.md`. This closes itself once the lanes are back within budget.",
    ].join("\n"),
  });
}

async function run() {
  const token = process.env.GITHUB_TOKEN;
  if (!token) throw new Error("GITHUB_TOKEN must allow reading Actions");
  const repository = process.env.GITHUB_REPOSITORY ?? "astilbahq/astilba";
  const request = github(token);
  const isFamily = familyMatcher(readFileSync("Makefile", "utf8"));
  const kinds = [
    {
      heading: "Go race lanes",
      title: issueTitle,
      check: async () => {
        const jobs = await recentJobs(request, repository);
        const result = laneBudget(jobs);
        const table = [
          timesTable(result.rows),
          splitReport(nameSplit(jobs, isFamily)),
        ]
          .filter(Boolean)
          .join("\n\n");
        return { ...result, table };
      },
    },
    {
      heading: "Native lanes",
      title: nativeIssueTitle,
      check: async () => {
        const jobs = await recentJobs(request, repository, nativeLanes);
        const result = nativeLaneBudget(
          jobs,
          await commitBudgets(request, repository, jobs),
        );
        return { ...result, table: timesTable(result.rows, true) };
      },
    },
  ];
  // One kind failing to check or report still lets the other report.
  const results = [];
  const failures = [];
  for (const { check, ...kind } of kinds)
    try {
      const result = await check();
      await report(request, repository, { ...kind, ...result });
      results.push(result);
    } catch (error) {
      failures.push(`${kind.heading}: ${error.message}`);
    }

  // In a workflow the issue is the notice, so the job fails only when it could not
  // check or report, which the scheduled scans' check on this workflow reports. Run by
  // hand, a lane past its budget fails.
  const problems = results.flatMap((result) => result.problems);
  if (failures.length || problems.length) {
    const text = [...failures, ...problems.map(({ line }) => line)].join("\n");
    if (
      failures.length ||
      !process.env.GITHUB_RUN_ID ||
      problems.some(({ check }) => check)
    )
      throw new Error(text);
    console.log(text);
    return;
  }
  console.log(
    results.some(({ undecided }) => undecided)
      ? `No lane with ${minimumRuns} runs is past its budget, and some have fewer.`
      : `Every lane's packages are within budget: ${budgetSeconds}s for a Go race lane, and ${nativeShare * 100}% of its command's -timeout for a native one.`,
  );
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  try {
    await run();
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
