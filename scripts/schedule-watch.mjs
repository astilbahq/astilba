import { pathToFileURL } from "node:url";
import { github, syncIssue } from "./report-issue.mjs";

// Each daily workflow checks on the other, because a workflow that cannot start, or
// whose report job fails, cannot say so itself: GitHub sends that notice only to the
// account behind the schedule. See docs/continuous-integration.md#scheduled-reports.

// GitHub starts a daily schedule hours late under load, so a run is missed only past
// a day and a half.
export const windowMs = 36 * 3_600_000;
// Runs the concurrency group cancelled before they started say nothing, so a few
// finished runs are read to find one that does.
const judgedRuns = 5;

// The workflow's scheduled runs on main since its file last changed there, newest
// first. Only scheduled runs count: they always run main's file, so no fork's runs,
// and no manual run on another ref, can stand in for one. A run from before the file
// changed ran other jobs or steps, so it is not judged by these.
export function ownRuns(runs, repository, changed) {
  return runs
    .filter(
      (run) =>
        run.event === "schedule" &&
        run.head_branch === "main" &&
        run.head_repository?.full_name === repository &&
        Date.parse(run.created_at) >= Date.parse(changed),
    )
    .toSorted((a, b) => b.created_at.localeCompare(a.created_at));
}

// What a finished run says about the jobs that had to succeed: null when they did, a
// problem when they did not (a job that timed out included), and undefined when the run
// was cancelled before any of them ran, so the next one is judged instead.
export function outcome(run, jobs, names) {
  if (run.conclusion === "startup_failure")
    return {
      key: "startup failure",
      line: `- The run could not start: ${run.html_url}`,
    };
  const conclusions = names.map(
    (name) => jobs.find((job) => job.name === name)?.conclusion,
  );
  if (
    run.conclusion === "cancelled" &&
    conclusions.every((conclusion) => !conclusion || conclusion === "skipped")
  )
    return undefined;
  const failed = names.flatMap((name, index) =>
    conclusions[index] === "success"
      ? []
      : [`${name} ended ${conclusions[index] ?? "without running"}`],
  );
  return failed.length
    ? { key: "job failed", line: `- ${failed.join("; ")}: ${run.html_url}` }
    : null;
}

// What is wrong with a workflow: disabled, what its newest judged run says, or no
// judged run for 36 hours (counted from the file's last change on main when it has none
// since). A fresh run still going leaves the issue as it is rather than closing or
// opening it on an old verdict.
export function stopped({ workflow, changed, runs, judged, now }) {
  const problems = [];
  if (workflow.state !== "active")
    problems.push({
      key: "disabled",
      line: `- GitHub reports the workflow as ${workflow.state}`,
    });
  if (judged?.problem) problems.push(judged.problem);
  const fresh = (date) => now - Date.parse(date) <= windowMs;
  const since = judged?.run.created_at ?? changed;
  if (fresh(since)) return { problems, undecided: !judged && !problems.length };
  if (
    runs.some(
      ({ status, created_at }) => status !== "completed" && fresh(created_at),
    )
  )
    return { problems, undecided: !problems.length };
  problems.push({
    key: "no run",
    line: judged
      ? `- No run since ${since}: ${judged.run.html_url}`
      : `- No run since the workflow last changed on main, at ${since}`,
  });
  return { problems, undecided: false };
}

async function main() {
  const [file, title, ...names] = process.argv.slice(2);
  const token = process.env.GITHUB_TOKEN;
  const repository = process.env.GITHUB_REPOSITORY;
  if (!file || !title || !names.length || !token || !repository)
    throw new Error(
      "usage: GITHUB_TOKEN=… GITHUB_REPOSITORY=… schedule-watch.mjs <workflow file> <issue title> <job name>…",
    );
  const request = github(token);
  const get = async (path) => (await request(path)).json();
  const base = `/repos/${repository}/actions/workflows/${file}`;
  const workflow = await get(base);
  const [commit] = await get(
    `/repos/${repository}/commits?sha=main&path=${encodeURIComponent(workflow.path)}&per_page=1`,
  );
  const changed = commit?.commit.committer.date ?? workflow.created_at;
  const runs = ownRuns(
    (await get(`${base}/runs?event=schedule&per_page=30`)).workflow_runs,
    repository,
    changed,
  );
  let judged;
  for (const run of runs
    .filter(({ status }) => status === "completed")
    .slice(0, judgedRuns)) {
    const { jobs } = await get(
      `/repos/${repository}/actions/runs/${run.id}/jobs?per_page=100`,
    );
    const problem = outcome(run, jobs, names);
    if (problem !== undefined) {
      judged = { run, problem };
      break;
    }
  }
  const { problems, undecided } = stopped({
    workflow,
    changed,
    runs,
    judged,
    now: Date.now(),
  });
  const result = await syncIssue(request, repository, {
    title,
    problems,
    undecided,
    body: problems.length
      ? [
          `The daily check on \`${file}\` found it is not running or reporting as it should, so its own issues may be out of date.`,
          "",
          ...problems.map(({ line }) => line),
          "",
          "See `docs/continuous-integration.md#scheduled-reports`. This closes itself once it runs and reports again.",
        ].join("\n")
      : `\`${file}\` ran and reported again.`,
  });
  console.log(`${file}: ${problems.length} problems, issue ${result}`);
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  try {
    await main();
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
