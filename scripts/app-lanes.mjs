import { readFileSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import {
  compile,
  goTests,
  linuxTests,
  matrixProblems,
  parseMakefile,
  selectionKey,
  selectionProblem,
  selectTests,
} from "./native-lanes.mjs";

const workflowPath = ".github/workflows/application.yml";
const aggregate = "verify-app-core";
const lanePrefix = "verify-app-go-";

function workflowProblems(workflow, lanes) {
  const problems = matrixProblems(
    workflow,
    workflowPath,
    lanes.map((name) => name.replace(lanePrefix, "")),
    aggregate,
  );
  const step =
    /^( *)- run: make verify-app-go-\$\{\{ matrix\.lane \}\}[ \t]*\n((?:\1 {2}.*\n)*)/m.exec(
      workflow,
    );
  if (!step || /^\s*(?:if|continue-on-error):/m.test(step[2]))
    problems.push(
      `${workflowPath} needs an unconditional step: run: make ${lanePrefix}\${{ matrix.lane }}`,
    );
  return problems;
}

// Every Go test in the platform module runs in exactly one application lane. A lane
// without -run takes whatever the others leave, through its -skip, so a new test
// lands there.
export function appLaneProblems({ makefile, workflow, tests }) {
  // `make check-ci` runs the native guard first, which reports what this cannot read.
  const { recipes, expand } = parseMakefile(makefile);
  const problems = [];
  const lanes = [...recipes.keys()].filter((name) =>
    name.startsWith(lanePrefix),
  );

  const lines = recipes.get(aggregate) ?? [];
  const parallel =
    lines.length === 1
      ? /^[@+]*\$\(MAKE\) -j\d+((?: \S+)+)$/.exec(lines[0])
      : null;
  if (!parallel)
    problems.push(
      `${aggregate} must run only $(MAKE) -jN and its lanes, found: ${lines.join("; ")}`,
    );
  else {
    const ran = parallel[1].trim().split(" ");
    for (const name of ran)
      if (!lanes.includes(name))
        problems.push(`${aggregate} runs ${name}, which is not a lane rule`);
    for (const name of lanes)
      if (!ran.includes(name))
        problems.push(`${name} is not run by ${aggregate}`);
  }
  problems.push(...workflowProblems(workflow, lanes));

  const commands = [];
  let refused = false;
  for (const lane of lanes)
    try {
      const found = recipes.get(lane).flatMap((line) => goTests(expand(line)));
      if (!found.length) problems.push(`${lane} runs no go test`);
      for (const { runs, skips, command, packages } of found) {
        if (runs.length > 1 || skips.length > 1)
          throw new Error(`expected at most one -run and -skip: ${command}`);
        commands.push({
          lane,
          label: lane,
          packages,
          run: runs.flatMap(compile),
          skip: skips.flatMap(compile),
        });
      }
    } catch (error) {
      problems.push(`${lane}: ${error.message}`);
      refused = true;
    }
  // What a refused lane selects is unknown, so reporting its tests as unselected
  // would be false.
  if (refused) return problems;

  const { problems: unmatched, selections } = selectTests(commands, tests);
  problems.push(...unmatched);
  for (const test of tests) {
    const selected = selections.get(selectionKey(test)) ?? [];
    const problem = selectionProblem(test, selected, lanePrefix);
    if (problem) problems.push(problem);
  }
  return problems;
}

function run() {
  const cwd = process.cwd();
  // The application lanes run on amd64 alone, and TestMain is not a test.
  const tests = linuxTests(cwd, "amd64").filter(
    (test) => test.name !== "TestMain",
  );
  const problems = appLaneProblems({
    makefile: readFileSync(join(cwd, "Makefile"), "utf8"),
    workflow: readFileSync(join(cwd, workflowPath), "utf8"),
    tests,
  });
  if (problems.length) throw new Error(problems.join("\n"));
  console.log(`Application lanes select each Go test once (${tests.length}).`);
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  try {
    run();
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
