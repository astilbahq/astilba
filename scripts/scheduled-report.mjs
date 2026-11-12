import { existsSync, readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { github, inert, renderSections, syncIssue } from "./report-issue.mjs";

// The Scheduled scans workflow's report job, the only job there that may write issues.
// It reads what the scan jobs wrote as data, since they ran third-party scanners and
// images: their text reaches the public issue only as inert lines.

// Each problem's key takes 13 characters of the issue body, so 3000 leave room for it.
const limits = { entries: 3000, key: 500, text: 4000 };

export const reports = [
  {
    name: "dependencies",
    title: "Scheduled vulnerability scan failed",
    what: "scheduled dependency vulnerability scan",
    docs: "docs/continuous-integration.md#vulnerability-scan",
    healthy: "the scheduled scan passes",
  },
  {
    name: "pins",
    title: "Pinned images and tools need attention",
    what: "pinned image and tool report",
    docs: "docs/continuous-integration.md#pinned-images-and-tools",
    healthy: "nothing pinned needs attention",
    // Much of it waits on upstream, so closing the issue accepts what it lists.
    acknowledge: true,
  },
];

const entry = (value, keyed) =>
  value !== null &&
  typeof value === "object" &&
  (value.section === undefined || typeof value.section === "string") &&
  typeof value.text === "string" &&
  value.text.length <= limits.text &&
  (!keyed ||
    (typeof value.key === "string" &&
      value.key.length > 0 &&
      value.key.length <= limits.key));

// A report a scan job wrote, or undefined when it is missing or not of this shape.
export function readReport(text) {
  try {
    const { problems, summary = problems, info = [] } = JSON.parse(text);
    if (
      [problems, summary, info].every(Array.isArray) &&
      problems.length <= limits.entries &&
      summary.length + info.length <= limits.entries &&
      problems.every((value) => entry(value, true)) &&
      [...summary, ...info].every((value) => entry(value, false))
    )
      return { problems, summary, info };
  } catch {}
  return undefined;
}

// A job that passed closes its issue. One that failed reports what it found, or that
// it did not finish, which is just as much a reason to look. A skipped job decides
// nothing.
export function decide(result, report, what) {
  const nothing = { problems: [], summary: [], info: [] };
  if (result === "success") return { ...nothing, undecided: false };
  if (result !== "failure" && result !== "cancelled")
    return { ...nothing, undecided: true };
  if (report?.problems.length) return { ...report, undecided: false };
  const unfinished = {
    section: "Did not finish",
    key: "unfinished",
    text: `The ${what} ${result === "cancelled" ? "was cancelled or timed out" : "failed without a finding to report, for example on a download, a check or a crash"}. Its log has the reason.`,
  };
  return {
    problems: [unfinished],
    summary: [unfinished],
    info: report?.info ?? [],
    undecided: false,
  };
}

export function issueBody(
  { what, docs, healthy, acknowledge },
  decision,
  runUrl,
  date,
) {
  if (!decision.problems.length)
    return `The ${what} passed on ${date}: ${runUrl}`;
  return [
    `The ${what} on ${date} found problems.`,
    "",
    renderSections({ problems: decision.summary, info: decision.info }),
    "",
    `Run: ${runUrl}`,
    "",
    `See \`${docs}\`. This closes itself once ${healthy}.${acknowledge ? " Closing it accepts what it lists: a new problem opens a new issue." : ""}`,
  ].join("\n");
}

// Brings one report's issue up to date with its job's result and what it wrote.
export function syncReport(request, repository, report, result, written, run) {
  const decision = decide(result, written, report.what);
  return syncIssue(request, repository, {
    title: report.title,
    undecided: decision.undecided,
    acknowledge: report.acknowledge ?? false,
    problems: decision.problems.map(({ key, text }) => ({
      key,
      line: `- ${inert(text, 600)}`,
    })),
    body: issueBody(report, decision, run.url, run.date),
  });
}

async function main() {
  const token = process.env.GITHUB_TOKEN;
  const repository = process.env.GITHUB_REPOSITORY;
  if (!token || !repository)
    throw new Error("GITHUB_TOKEN and GITHUB_REPOSITORY must be set");
  const request = github(token);
  const run = {
    url: `${process.env.GITHUB_SERVER_URL}/${repository}/actions/runs/${process.env.GITHUB_RUN_ID}`,
    date: new Date().toISOString().slice(0, 10),
  };
  let failed = false;
  for (const report of reports) {
    const prefix = report.name.toUpperCase();
    const result = process.env[`${prefix}_RESULT`];
    const path = process.env[`${prefix}_REPORT`];
    const present = Boolean(path) && existsSync(path);
    const written = present
      ? readReport(readFileSync(path, "utf8"))
      : undefined;
    if (present && !written)
      console.warn(`::warning::${path} is not a report; ignoring it`);
    try {
      const outcome = await syncReport(
        request,
        repository,
        report,
        result,
        written,
        run,
      );
      console.log(`${report.title}: ${result}, issue ${outcome}`);
    } catch (error) {
      console.error(`${report.title}: ${error.message}`);
      failed = true;
    }
  }
  if (failed) process.exitCode = 1;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href)
  await main();
