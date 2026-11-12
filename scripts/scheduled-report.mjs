import { existsSync, readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { github, inert, renderSections, syncIssue } from "./report-issue.mjs";

// The Scheduled scans workflow's report job, the only job there that may write issues.
// It reads what the scan jobs wrote as data, since they ran third-party scanners and
// images: their text reaches the public issue only as inert lines.

const limits = { entries: 2000, key: 500, text: 4000 };

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
    const { problems, info = [] } = JSON.parse(text);
    if (
      Array.isArray(problems) &&
      Array.isArray(info) &&
      problems.length + info.length <= limits.entries &&
      problems.every((value) => entry(value, true)) &&
      info.every((value) => entry(value, false))
    )
      return { problems, info };
  } catch {}
  return undefined;
}

// A job that passed closes its issue. One that failed reports what it found, or that
// it did not finish, which is just as much a reason to look. A skipped job decides
// nothing.
export function decide(result, report, what) {
  if (result === "success") return { problems: [], info: [], undecided: false };
  if (result !== "failure" && result !== "cancelled")
    return { problems: [], info: [], undecided: true };
  if (report?.problems.length) return { ...report, undecided: false };
  return {
    problems: [
      {
        section: "Did not finish",
        key: "unfinished",
        text: `The ${what} ${result === "cancelled" ? "was cancelled or timed out" : "failed without a finding to report, for example on a download, a check or a crash"}. Its log has the reason.`,
      },
    ],
    info: report?.info ?? [],
    undecided: false,
  };
}

export function issueBody({ what, docs, healthy }, decision, runUrl, date) {
  if (!decision.problems.length)
    return `The ${what} passed on ${date}: ${runUrl}`;
  return [
    `The ${what} on ${date} found problems.`,
    "",
    renderSections(decision),
    "",
    `Run: ${runUrl}`,
    "",
    `See \`${docs}\`. This closes itself once ${healthy}.`,
  ].join("\n");
}

async function main() {
  const token = process.env.GITHUB_TOKEN;
  const repository = process.env.GITHUB_REPOSITORY;
  if (!token || !repository)
    throw new Error("GITHUB_TOKEN and GITHUB_REPOSITORY must be set");
  const request = github(token);
  const runUrl = `${process.env.GITHUB_SERVER_URL}/${repository}/actions/runs/${process.env.GITHUB_RUN_ID}`;
  const date = new Date().toISOString().slice(0, 10);
  let failed = false;
  for (const report of reports) {
    const prefix = report.name.toUpperCase();
    const result = process.env[`${prefix}_RESULT`];
    const path = process.env[`${prefix}_REPORT`];
    const written =
      path && existsSync(path)
        ? readReport(readFileSync(path, "utf8"))
        : undefined;
    if (path && existsSync(path) && !written)
      console.warn(`::warning::${path} is not a report; ignoring it`);
    const decision = decide(result, written, report.what);
    try {
      const outcome = await syncIssue(request, repository, {
        title: report.title,
        undecided: decision.undecided,
        problems: decision.problems.map(({ key, text }) => ({
          key,
          line: `- ${inert(text, 600)}`,
        })),
        body: issueBody(report, decision, runUrl, date),
      });
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
