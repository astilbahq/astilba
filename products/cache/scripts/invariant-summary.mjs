// Reads a Vitest JSON report for the `invariants` project and prints a Markdown
// summary (CI redirects stdout to $GITHUB_STEP_SUMMARY). An invariant counts as
// "passing" only when every test citing `INV-<n>` passed; scenarios are the
// red-team-five tests under test/scenarios/. In the kernel phase this reads
// "0/29 passing + 5 scenarios pending".

import { readFileSync } from "node:fs";

const EXPECTED_INVARIANTS = 29;
const reportPath = process.argv[2] ?? "invariants-report.json";

let report;
try {
  report = JSON.parse(readFileSync(reportPath, "utf-8"));
} catch (error) {
  console.log(
    `## Invariant suite\n\n⚠️ could not read \`${reportPath}\`: ${error.message}`
  );
  process.exit(0);
}

const inv = new Map(); // n -> { total, passed }
let scenarioTotal = 0;
let scenarioPassed = 0;

for (const file of report.testResults ?? []) {
  const isScenario = /test\/scenarios\//.test(file.name ?? "");
  for (const a of file.assertionResults ?? []) {
    const passed = a.status === "passed";
    if (isScenario) {
      scenarioTotal += 1;
      if (passed) {
        scenarioPassed += 1;
      }
      continue;
    }
    const label = `${(a.ancestorTitles ?? []).join(" ")} ${a.title ?? a.fullName ?? ""}`;
    for (const match of label.matchAll(/INV-(\d+)/g)) {
      const n = Number(match[1]);
      const rec = inv.get(n) ?? { total: 0, passed: 0 };
      rec.total += 1;
      if (passed) {
        rec.passed += 1;
      }
      inv.set(n, rec);
    }
  }
}

let invariantsPassing = 0;
const missing = [];
for (let n = 1; n <= EXPECTED_INVARIANTS; n += 1) {
  const rec = inv.get(n);
  if (!rec || rec.total === 0) {
    missing.push(n); // absent id ⇒ a likely config/collection break, surfaced honestly
    continue;
  }
  if (rec.passed === rec.total) {
    invariantsPassing += 1;
  }
}

const scenarioPending = scenarioTotal - scenarioPassed;
const headline = `${invariantsPassing}/${EXPECTED_INVARIANTS} passing + ${scenarioPending} scenarios pending`;

const lines = ["## Invariant suite", "", `**${headline}**`, ""];
if (missing.length > 0) {
  lines.push(
    `> ⚠️ invariant ids with no collected test: ${missing.join(", ")} (expected 0 — investigate collection/config).`,
    ""
  );
}
lines.push(
  `- Invariants passing: **${invariantsPassing} / ${EXPECTED_INVARIANTS}**`,
  `- Scenarios (red-team-five) pending: **${scenarioPending} / ${scenarioTotal}**`
);

console.log(lines.join("\n"));
