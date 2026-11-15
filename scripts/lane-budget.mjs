import { appendFileSync, readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { compile, parseMakefile } from "./native-lanes.mjs";
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
// keeps the issue's earlier problems.
export function laneBudget(jobs, budget = budgetSeconds) {
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
      };
    })
    .toSorted((a, b) => b.seconds - a.seconds);
  for (const { lane, name, seconds, samples: count } of rows)
    if (count >= minimumRuns && seconds > budget)
      problem(
        `over\t${lane}\t${name}`,
        `${lane}: \`${name}\` has a median of ${seconds}s over ${count} runs, past the ${budget}s budget. Rebalance the lanes, as docs/continuous-integration.md describes.`,
      );
  const undecided = rows.some(({ samples: count }) => count < minimumRuns);
  return { problems, undecided, rows };
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
// as the last name lane takes whatever the others leave. When no split fits, this
// returns the closest, which gives each lettered range its own lane.
export function nameSplit(jobs, isFamily, target = targetSeconds) {
  const samples = new Map();
  const overheads = new Map();
  const add = (map, key, value) =>
    map.set(key, [...(map.get(key) ?? []), value]);
  for (const { times, tests = [] } of jobs) {
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
  for (let count = 1; count <= starts.length; count++) {
    const lanes = ranges(count);
    if (lanes.every(({ seconds }) => seconds <= target))
      return { fits: true, lanes };
    if (count === starts.length) return { fits: false, lanes };
  }
}

// The proposed split as Markdown, for the job summary and the issue.
export function splitReport(split, target = targetSeconds) {
  if (!split) return "";
  const { fits, lanes } = split;
  const worst = lanes.reduce((a, b) => (b.seconds > a.seconds ? b : a));
  return [
    fits
      ? `The fewest name lanes that keep every package within ${target}s, from the same runs' test times:`
      : `No split by first letter keeps every package within ${target}s: \`Test${worst.from}\` to \`Test${worst.to}\` alone is projected at ${Math.round(worst.seconds)}s. Move a family of those tests to a lane of its own. The closest split:`,
    "",
    "| Lane | Names | Slowest package | Projected seconds |",
    "| --- | --- | --- | --- |",
    ...lanes.map(
      ({ lane, from, to, package: name, seconds }) =>
        `| ${lane} | \`Test${from}\` to \`Test${to}\` | \`${name}\` | ${Math.round(seconds)} |`,
    ),
  ].join("\n");
}

// Main pushes run every lane once, and a newer run cannot cancel them. A run that
// ran other lanes predates a rebalancing, so its times would misreport today's lanes,
// and one with a lane that did not finish has no time for it.
export async function recentJobs(request, repository) {
  const { workflow_runs: runs } = await (
    await request(
      `/repos/${repository}/actions/workflows/application.yml/runs?branch=main&event=push&status=completed&per_page=${sampledRuns * 3}`,
    )
  ).json();
  let sampled = 0;
  let current;
  const jobs = [];
  for (const { id } of runs) {
    if (sampled === sampledRuns) break;
    const { jobs: listed } = await (
      await request(`/repos/${repository}/actions/runs/${id}/jobs?per_page=100`)
    ).json();
    const lanes = listed.flatMap((job) => {
      const lane = /^Go race \(([\w-]+)\)$/.exec(job.name)?.[1];
      return lane ? [{ ...job, lane }] : [];
    });
    if (!lanes.length) continue;
    const names = lanes
      .map(({ lane }) => lane)
      .sort()
      .join();
    current ??= names;
    if (
      names !== current ||
      !lanes.every(({ conclusion }) =>
        ["success", "failure"].includes(conclusion),
      )
    )
      continue;
    sampled++;
    for (const { id: job, lane, conclusion } of lanes) {
      const log = await (
        await request(`/repos/${repository}/actions/jobs/${job}/logs`)
      ).text();
      jobs.push({
        run: id,
        lane,
        conclusion,
        times: packageTimes(log),
        tests: testTimes(log),
      });
    }
  }
  return jobs;
}

async function run() {
  const token = process.env.GITHUB_TOKEN;
  if (!token) throw new Error("GITHUB_TOKEN must allow reading Actions");
  const repository = process.env.GITHUB_REPOSITORY ?? "astilbahq/astilba";
  const request = github(token);
  const isFamily = familyMatcher(readFileSync("Makefile", "utf8"));
  const jobs = await recentJobs(request, repository);
  const { problems, undecided, rows } = laneBudget(jobs);
  const times = [
    "| Lane | Package | Median seconds | Runs |",
    "| --- | --- | --- | --- |",
    ...rows
      .filter(({ seconds }) => seconds >= 10)
      .map(
        ({ lane, name, seconds, samples }) =>
          `| ${lane} | \`${name}\` | ${seconds} | ${samples} |`,
      ),
  ].join("\n");
  const table = [times, splitReport(nameSplit(jobs, isFamily))]
    .filter(Boolean)
    .join("\n\n");
  console.log(table);
  // Only the workflow reports, so a local run changes nothing.
  if (process.env.GITHUB_RUN_ID) {
    appendFileSync(process.env.GITHUB_STEP_SUMMARY, `${table}\n`);
    await syncIssue(request, repository, {
      title: issueTitle,
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
  // In a workflow the issue is the notice, so the job fails only when it could not
  // check or report, which the scheduled scans' check on this workflow reports. Run by
  // hand, a lane past its budget fails.
  if (problems.length) {
    const text = problems.map(({ line }) => line).join("\n");
    if (!process.env.GITHUB_RUN_ID || problems.some(({ check }) => check))
      throw new Error(text);
    console.log(text);
    return;
  }
  console.log(
    undecided
      ? `No lane with ${minimumRuns} runs is past ${budgetSeconds}s, and some have fewer.`
      : `Every lane's packages are within ${budgetSeconds}s.`,
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
