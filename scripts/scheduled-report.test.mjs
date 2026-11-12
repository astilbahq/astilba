import assert from "node:assert/strict";
import { test } from "node:test";
import { inert, syncIssue } from "./report-issue.mjs";
import { decide, issueBody, readReport, reports } from "./scheduled-report.mjs";
import { reportProblems } from "./vulnerability-scan.mjs";

test("a report that is not of the expected shape is not read", () => {
  const ok = { problems: [{ key: "k", text: "t", section: "Blocking" }] };
  assert.deepEqual(readReport(JSON.stringify(ok)), { ...ok, info: [] });
  for (const bad of [
    "not json",
    "{}",
    JSON.stringify({ problems: [{ text: "no key" }] }),
    JSON.stringify({ problems: [{ key: "k", text: 1 }] }),
    JSON.stringify({ problems: [{ key: "k", text: "t", section: 2 }] }),
    JSON.stringify({ problems: [{ key: "k".repeat(501), text: "t" }] }),
    JSON.stringify({ problems: [], info: [{ text: "t".repeat(4001) }] }),
  ])
    assert.equal(readReport(bad), undefined, bad);
});

test("a passed job closes, a failed one reports, and a skipped one decides nothing", () => {
  const report = { problems: [{ key: "k", text: "t" }], info: [] };
  assert.deepEqual(decide("success", report, "scan"), {
    problems: [],
    info: [],
    undecided: false,
  });
  assert.deepEqual(decide("failure", report, "scan"), { ...report, undecided: false });
  for (const result of ["failure", "cancelled"]) {
    const unfinished = decide(result, undefined, "scan");
    assert.equal(unfinished.undecided, false);
    assert.deepEqual(unfinished.problems.map(({ key }) => key), ["unfinished"]);
  }
  assert.equal(decide("failure", { problems: [], info: [] }, "scan").problems[0].key, "unfinished");
  assert.equal(decide("skipped", undefined, "scan").undecided, true);
  assert.equal(decide(undefined, undefined, "scan").undecided, true);
});

const fakeGitHub = () => {
  const issues = [];
  const sent = [];
  return {
    sent,
    request: async (path, { method = "GET", body } = {}) => {
      if (method !== "GET") sent.push({ method, path, ...body });
      if (method === "POST" && path === "/repos/o/r/issues")
        issues.push({ number: issues.length + 1, state: "open", ...body });
      const number = Number(/\/issues\/(\d+)$/.exec(path)?.[1]);
      if (method === "PATCH") Object.assign(issues[number - 1], body);
      return {
        json: async () => issues.filter(({ state }) => state === "open"),
      };
    },
  };
};

// The report job's own steps for the dependency scan, against a fake GitHub.
async function scheduledRun(github, result, written, date) {
  const [dependencies] = reports;
  const decision = decide(result, written && readReport(JSON.stringify(written)), dependencies.what);
  return syncIssue(github.request, "o/r", {
    title: dependencies.title,
    undecided: decision.undecided,
    problems: decision.problems.map(({ key, text }) => ({ key, line: `- ${inert(text, 600)}` })),
    body: issueBody(dependencies, decision, "https://github.test/run", date),
  });
}

test("a seeded scan failure opens one issue, a repeat updates it, recovery closes it", async () => {
  const blocked = {
    id: "GHSA-a",
    aliases: ["CVE-2026-1"],
    package: "left-pad",
    version: "1.0.0",
    fixed: "1.0.1",
    summary: "@someone [click](https://e.test)",
    reachable: true,
    surfaces: ["dashboard"],
    why: "not allowlisted",
  };
  const seeded = reportProblems({ blocked: [blocked], stale: [] });
  const github = fakeGitHub();

  assert.equal(await scheduledRun(github, "failure", seeded, "2026-11-12"), "opened");
  const [opened] = github.sent;
  assert.equal(opened.title, "Scheduled vulnerability scan failed");
  assert.match(opened.body, /### Blocking \(1\)/);
  assert.match(
    opened.body,
    /- `\[dashboard\] GHSA-a left-pad@1\.0\.0, fixed in 1\.0\.1: @someone \[click\]\(https:\/\/e\.test\) \(not allowlisted\)`/,
  );
  assert.match(opened.body, /Run: https:\/\/github\.test\/run/);

  assert.equal(await scheduledRun(github, "failure", seeded, "2026-11-13"), "updated");
  assert.equal(github.sent.at(-1).method, "PATCH");
  assert.match(github.sent.at(-1).body, /on 2026-11-13 found problems/);

  // A download failure the next day is a different problem, so it is announced.
  assert.equal(await scheduledRun(github, "failure", undefined, "2026-11-14"), "commented");
  assert.match(github.sent.at(-1).body, /New since the last report \(1\):\n\n- `The scheduled dependency/);

  assert.equal(await scheduledRun(github, "skipped", undefined, "2026-11-15"), "unchanged");

  assert.equal(await scheduledRun(github, "success", undefined, "2026-11-16"), "closed");
  assert.deepEqual(
    [github.sent.at(-1).state, github.sent.at(-1).state_reason],
    ["closed", "completed"],
  );
  assert.match(github.sent.at(-1).body, /passed on 2026-11-16: https:\/\/github\.test\/run/);

  // The next failure opens a new issue rather than reviving the closed one.
  assert.equal(await scheduledRun(github, "failure", seeded, "2026-11-17"), "opened");
});
