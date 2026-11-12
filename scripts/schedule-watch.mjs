import { pathToFileURL } from "node:url";
import { github, syncIssue } from "./report-issue.mjs";

// Each daily workflow checks on the other, because a workflow that cannot start, or
// whose report job fails, cannot say so itself: GitHub sends that notice only to the
// account behind the schedule. See docs/continuous-integration.md#scheduled-reports.

// GitHub starts a daily schedule hours late under load, so a run is missed only past
// a day and a half.
export const windowMs = 36 * 3_600_000;

// What is wrong with a workflow's runs on main, newest first: none lately, one that
// could not start, or a job that had to succeed and did not. A workflow with no runs
// yet, such as one just added, decides nothing.
export function stopped({ runs, jobs = [], job, now }) {
  if (!runs.length) return { problems: [], undecided: true };
  const problems = [];
  const [newest] = runs;
  if (now - Date.parse(newest.created_at) > windowMs)
    problems.push({
      key: "no run",
      line: `- No run since ${newest.created_at}: ${newest.html_url}`,
    });
  const finished = runs.find(({ status }) => status === "completed");
  if (finished?.conclusion === "startup_failure")
    problems.push({
      key: "startup failure",
      line: `- The run could not start: ${finished.html_url}`,
    });
  else if (job && finished) {
    const conclusion = jobs.find(({ name }) => name === job)?.conclusion;
    if (conclusion !== "success")
      problems.push({
        key: "job failed",
        line: `- ${job} ended ${conclusion ?? "without running"}: ${finished.html_url}`,
      });
  }
  return { problems, undecided: false, finished };
}

async function main() {
  const [workflow, title, job] = process.argv.slice(2);
  const token = process.env.GITHUB_TOKEN;
  const repository = process.env.GITHUB_REPOSITORY;
  if (!workflow || !title || !token || !repository)
    throw new Error(
      "usage: GITHUB_TOKEN=… GITHUB_REPOSITORY=… schedule-watch.mjs <workflow file> <issue title> [job name]",
    );
  const request = github(token);
  const { workflow_runs: runs } = await (
    await request(
      `/repos/${repository}/actions/workflows/${workflow}/runs?branch=main&per_page=20`,
    )
  ).json();
  const scheduled = runs.filter(({ event }) =>
    ["schedule", "workflow_dispatch"].includes(event),
  );
  const finished = scheduled.find(({ status }) => status === "completed");
  const jobs =
    job && finished
      ? (
          await (
            await request(
              `/repos/${repository}/actions/runs/${finished.id}/jobs?per_page=100`,
            )
          ).json()
        ).jobs
      : [];
  const { problems, undecided } = stopped({
    runs: scheduled,
    jobs,
    job,
    now: Date.now(),
  });
  const outcome = await syncIssue(request, repository, {
    title,
    problems,
    undecided,
    body: problems.length
      ? [
          `\`${workflow}\` has stopped reporting, so its own issues may be out of date.`,
          "",
          ...problems.map(({ line }) => line),
          "",
          "See `docs/continuous-integration.md#scheduled-reports`. This closes itself once it runs and reports again.",
        ].join("\n")
      : `\`${workflow}\` ran and reported again.`,
  });
  console.log(`${workflow}: ${problems.length} problems, issue ${outcome}`);
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
