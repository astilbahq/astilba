import { appendFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { github, syncIssue } from "./report-issue.mjs";

// Each lane's `go test -timeout` allows 900 seconds. One commit's package time has
// differed by 28% between hosted runners, so a lane is moved well before a slow
// runner can reach the limit. A failure here cannot eject a pull request, because
// only the Lane budget workflow reads these times.
export const budgetSeconds = 600;
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
      jobs.push({ run: id, lane, conclusion, times: packageTimes(log) });
    }
  }
  return jobs;
}

async function run() {
  const token = process.env.GITHUB_TOKEN;
  if (!token) throw new Error("GITHUB_TOKEN must allow reading Actions");
  const repository = process.env.GITHUB_REPOSITORY ?? "astilbahq/astilba";
  const request = github(token);
  const { problems, undecided, rows } = laneBudget(
    await recentJobs(request, repository),
  );
  const table = [
    "| Lane | Package | Median seconds | Runs |",
    "| --- | --- | --- | --- |",
    ...rows
      .filter(({ seconds }) => seconds >= 10)
      .map(
        ({ lane, name, seconds, samples }) =>
          `| ${lane} | \`${name}\` | ${seconds} | ${samples} |`,
      ),
  ].join("\n");
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
