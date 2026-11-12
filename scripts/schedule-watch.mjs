import { pathToFileURL } from "node:url";
import { github, syncIssue } from "./report-issue.mjs";

// Each daily workflow checks on the other, because a workflow that cannot start, or
// whose report job fails, cannot say so itself: GitHub sends that notice only to the
// account behind the schedule. See docs/continuous-integration.md#scheduled-reports.

// GitHub starts a daily schedule hours late under load, so a run is missed only past
// a day and a half.
export const windowMs = 36 * 3_600_000;

// What is wrong with a workflow, given its own scheduled and manual runs on main, newest
// first: disabled, no run lately (or ever, once it has existed that long), a run that
// could not start, or a job or step that had to succeed and did not. A run the
// concurrency group cancelled says nothing either way. A workflow added within the
// window, with no run yet, decides nothing.
export function stopped({ workflow, runs, jobs = [], job, step, now }) {
  const problems = [];
  const problem = (key, line) => problems.push({ key, line: `- ${line}` });
  if (workflow.state !== "active")
    problem("disabled", `GitHub reports the workflow as ${workflow.state}`);
  const counted = runs.filter(({ conclusion }) => conclusion !== "cancelled");
  const [newest] = counted;
  const since = newest?.created_at ?? workflow.created_at;
  if (now - Date.parse(since) > windowMs)
    problem(
      "no run",
      newest
        ? `No run since ${since}: ${newest.html_url}`
        : `No run since it was added on ${since}`,
    );
  else if (!newest) return { problems, undecided: !problems.length };
  const finished = counted.find(({ status }) => status === "completed");
  if (finished?.conclusion === "startup_failure")
    problem("startup failure", `The run could not start: ${finished.html_url}`);
  else if (job && finished) {
    const found = jobs.find(({ name }) => name === job);
    const conclusion = step
      ? found?.steps?.find(({ name }) => name === step)?.conclusion
      : found?.conclusion;
    if (conclusion !== "success")
      problem(
        "job failed",
        `${step ?? job} ended ${conclusion ?? "without running"}: ${finished.html_url}`,
      );
  }
  return { problems, undecided: false, finished };
}

async function main() {
  const [file, title, job, step] = process.argv.slice(2);
  const token = process.env.GITHUB_TOKEN;
  const repository = process.env.GITHUB_REPOSITORY;
  if (!file || !title || !token || !repository)
    throw new Error(
      "usage: GITHUB_TOKEN=… GITHUB_REPOSITORY=… schedule-watch.mjs <workflow file> <issue title> [job name [step name]]",
    );
  const request = github(token);
  const get = async (path) => (await request(path)).json();
  const base = `/repos/${repository}/actions/workflows/${file}`;
  const workflow = await get(base);
  // Asked for by event, so other runs, such as a fork's pull requests from a branch it
  // calls main, cannot push these off the page.
  const runs = (
    await Promise.all(
      ["event=schedule", "event=workflow_dispatch&branch=main"].map(
        async (query) =>
          (await get(`${base}/runs?${query}&per_page=20`)).workflow_runs,
      ),
    )
  )
    .flat()
    .filter(
      (run) =>
        run.head_branch === "main" &&
        run.head_repository?.full_name === repository,
    )
    .toSorted((a, b) => b.created_at.localeCompare(a.created_at));
  const finished = runs.find(
    ({ status, conclusion }) =>
      status === "completed" && conclusion !== "cancelled",
  );
  const jobs =
    job && finished
      ? (
          await get(
            `/repos/${repository}/actions/runs/${finished.id}/jobs?per_page=100`,
          )
        ).jobs
      : [];
  const { problems, undecided } = stopped({
    workflow,
    runs,
    jobs,
    job,
    step,
    now: Date.now(),
  });
  const outcome = await syncIssue(request, repository, {
    title,
    problems,
    undecided,
    body: problems.length
      ? [
          `\`${file}\` has stopped reporting, so its own issues may be out of date.`,
          "",
          ...problems.map(({ line }) => line),
          "",
          "See `docs/continuous-integration.md#scheduled-reports`. This closes itself once it runs and reports again.",
        ].join("\n")
      : `\`${file}\` ran and reported again.`,
  });
  console.log(`${file}: ${problems.length} problems, issue ${outcome}`);
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
