import { appendFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

// Each lane's `go test -timeout` allows 900 seconds. One commit's package time has
// differed by 28% between hosted runners, so a lane is moved well before a slow
// runner can reach the limit. A failure here cannot eject a pull request, because
// only the Lane budget workflow reads these times.
export const budgetSeconds = 600;
const sampledRuns = 5;
const minimumRuns = 3;
const api = "https://api.github.com";
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
export function laneBudget(jobs, budget = budgetSeconds) {
  const problems = [];
  const samples = new Map();
  for (const { run, lane, conclusion, times } of jobs) {
    if (conclusion === "success" && !times.length)
      problems.push(`run ${run}: ${lane} succeeded with no package times`);
    for (const { name, seconds } of times) {
      const key = `${lane}\t${name}`;
      samples.set(key, [...(samples.get(key) ?? []), seconds]);
    }
  }
  if (!samples.size) problems.push("found no lane times to compare");
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
      problems.push(
        `${lane}: \`${name}\` has a median of ${seconds}s over ${count} runs, past the ${budget}s budget. Rebalance the lanes, as docs/continuous-integration.md describes.`,
      );
  const undecided = rows.some(({ samples: count }) => count < minimumRuns);
  return { problems, undecided, rows };
}

function github(token) {
  return async (path, { method = "GET", body } = {}) => {
    const response = await fetch(`${api}${path}`, {
      method,
      headers: {
        accept: "application/vnd.github+json",
        authorization: `Bearer ${token}`,
      },
      body: body && JSON.stringify(body),
    });
    if (!response.ok)
      throw new Error(`${method} ${path} returned ${response.status}`);
    return response;
  };
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

// A person hears of a failure through this issue, because a scheduled run's own
// failure notice goes to whichever account GitHub holds responsible for the schedule.
// It stays open, once, for as long as the budget is exceeded, and says what is over
// today. Only this workflow's own issues count, as anyone can open one with this title.
export async function syncIssue(
  request,
  repository,
  { problems, undecided },
  body,
) {
  const issues = await (
    await request(
      `/repos/${repository}/issues?state=open&creator=github-actions%5Bbot%5D&per_page=100`,
    )
  ).json();
  const open = issues.find(({ title }) => title === issueTitle);
  if (problems.length && !open)
    await request(`/repos/${repository}/issues`, {
      method: "POST",
      body: { title: issueTitle, body },
    });
  else if (problems.length)
    await request(`/repos/${repository}/issues/${open.number}`, {
      method: "PATCH",
      body: { body },
    });
  else if (open && !undecided)
    await request(`/repos/${repository}/issues/${open.number}`, {
      method: "PATCH",
      body: { state: "closed", state_reason: "completed" },
    });
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
    await syncIssue(
      request,
      repository,
      { problems, undecided },
      [
        ...problems,
        "",
        table,
        "",
        `${process.env.GITHUB_SERVER_URL}/${repository}/actions/runs/${process.env.GITHUB_RUN_ID}`,
        "",
        "See the lane budget in `docs/continuous-integration.md`. This closes itself once the lanes are back within budget.",
      ].join("\n"),
    );
  }
  if (problems.length) throw new Error(problems.join("\n"));
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
