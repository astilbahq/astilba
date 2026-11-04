import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join, posix, relative } from "node:path";
import { pathToFileURL } from "node:url";

const root = "services/platform";
const workflowPath = ".github/workflows/ci.yml";
const aggregate = "verify-agent-host";
const lanePrefix = `${aggregate}-`;
const architectures = ["amd64", "arm64"];
// Re-executed inside an isolated host process or network namespace by the test
// that needs them; run directly they only skip.
const helpers = new Map([
  ["TestNativeCrossHostHelper", "services/platform/internal/agent"],
  ["TestNativeEndpointHelper", "services/platform/internal/hostnetwork"],
]);

// Marks a make reference this check cannot resolve. Make reads an unset one as
// empty and the command line can set it, so it only matters in `go test` arguments.
const unresolved = "\0";

// Reads only assignments and rules. Anything else, such as a conditional, a define
// or `+=`, could change what a lane runs, so it is reported as unread.
function parseMakefile(text) {
  const variables = new Map();
  const recipes = new Map();
  const unread = [];
  let current;
  for (const line of text.replace(/\\\n\s*/g, " ").split("\n")) {
    if (line.startsWith("\t")) {
      current?.push(line.trim());
      continue;
    }
    const content = line.replace(/#.*/, "").trim();
    const variable = /^([\w.-]+)\s*(?:::?|\?)?=\s*(.*)$/.exec(content);
    const rule = /^([^\s:=]+(?:\s+[^\s:=]+)*)\s*:(?!=)\s*(.*)$/.exec(content);
    if (variable) variables.set(variable[1], variable[2]);
    else if (rule) {
      // The aggregate's targets are read from its recipe alone.
      if (rule[1] === aggregate && rule[2]) unread.push(content);
      current = [];
      for (const target of rule[1].split(/\s+/)) recipes.set(target, current);
    } else if (content) unread.push(content);
  }
  const expand = (value) =>
    value.replace(
      /\$(?:\$|\(([^()]*)\)|\{([^{}]*)\}|.)/g,
      (reference, parenthesised, braced) => {
        if (reference === "$$") return "$";
        const known = variables.get(parenthesised ?? braced);
        return known === undefined
          ? `${unresolved}${reference}${unresolved}`
          : expand(known);
      },
    );
  return { recipes, unread, expand };
}

const goTestFlag = /^-(?:race|v|count=[1-9]\d*|timeout=\S+)$/;
const commandPrefix = /^(?:sudo|env|-\S*|\w+=\S*)$/;
const shellSyntax = /[;|&<>#()`]/;
const quotedOrUnresolved = new RegExp(
  `'[^']*'|"[^"]*"|${unresolved}[^${unresolved}]*${unresolved}`,
  "g",
);
const isGoTest = (word, next) => /(^|\/)go$/.test(word) && next === "test";

// The `go test` commands of one expanded recipe line. A line that runs go test
// must be a plain `cd DIR && ... go test` chain, because any other shell syntax or
// flag could stop the tests running while the line still names them.
function goTests(line) {
  const words =
    line.replace(/^[@+-]+/, "").match(/(?:'[^']*'|"[^"]*"|[^\s'"])+/g) ?? [];
  if (!words.some((word, i) => isGoTest(word, words[i + 1]))) return [];
  const syntax = words.find(
    (word) =>
      word !== "&&" && shellSyntax.test(word.replace(quotedOrUnresolved, "")),
  );
  if (syntax) throw new Error(`cannot read shell syntax ${syntax}: ${line}`);
  const commands = [[]];
  for (const word of words)
    if (word === "&&") commands.push([]);
    else commands.at(-1).push(word.replace(/'([^']*)'|"([^"]*)"/g, "$1$2"));

  let directory = ".";
  const found = [];
  for (const command of commands) {
    if (command[0] === "cd" && command.length === 2) {
      directory = posix.join(directory, command[1]);
      continue;
    }
    const go = command.findIndex((word, i) => isGoTest(word, command[i + 1]));
    if (
      go < 0 ||
      !command.slice(0, go).every((word) => commandPrefix.test(word))
    )
      throw new Error(`expected only cd and go test: ${line}`);
    const args = command.slice(go + 2);
    if (args.some((arg) => arg.includes(unresolved)))
      throw new Error(
        `cannot resolve ${args.join(" ").replaceAll(unresolved, "")}`,
      );
    const runs = [];
    const packages = [];
    for (let i = 0; i < args.length; i++) {
      const run = /^-run(?:=(.*))?$/.exec(args[i]);
      if (run) runs.push(run[1] ?? args[++i]);
      else if (args[i].startsWith("./"))
        packages.push(posix.join(directory, args[i]));
      else if (!goTestFlag.test(args[i]))
        throw new Error(`cannot read go test argument ${args[i]}`);
    }
    if (runs.length !== 1)
      throw new Error(`expected one -run: ${command.join(" ")}`);
    if (!packages.length)
      throw new Error(`expected a ./ package: ${command.join(" ")}`);
    found.push({ pattern: runs[0], packages });
  }
  return found;
}

// Distributes groups across their surroundings, so that each alternative of a
// pattern can be tested alone: `^T(a|b)c$` becomes `^Tac$` and `^Tbc$`. Syntax
// this cannot distribute is refused rather than treated as one alternative.
export function alternatives(pattern) {
  if (pattern.includes("/"))
    throw new Error(`-run selects whole tests, not subtests: ${pattern}`);
  let i = 0;
  const parse = (grouped) => {
    const finished = [];
    let sequences = [""];
    while (i < pattern.length) {
      const char = pattern[i++];
      if (char === ")") {
        if (!grouped) throw new Error(`unbalanced group: ${pattern}`);
        return [...finished, ...sequences];
      }
      if (char === "|") {
        finished.push(...sequences);
        sequences = [""];
        continue;
      }
      let pieces = [char];
      if (char === "\\") pieces = [char + (pattern[i++] ?? "")];
      else if (char === "[") {
        const set = /^\^?\]?(?:\\.|[^\]\\])*\]/.exec(pattern.slice(i));
        if (!set) throw new Error(`unterminated set: ${pattern}`);
        pieces = [char + set[0]];
        i += set[0].length;
      } else if (char === "(") {
        if (pattern[i] === "?") {
          if (pattern[i + 1] !== ":")
            throw new Error(`unsupported group: ${pattern}`);
          i += 2;
        }
        pieces = parse(true);
        if (/[*+?{]/.test(pattern[i] ?? ""))
          throw new Error(`cannot split a quantified group: ${pattern}`);
      }
      sequences = sequences.flatMap((prefix) =>
        pieces.map((piece) => prefix + piece),
      );
    }
    if (grouped) throw new Error(`unbalanced group: ${pattern}`);
    return [...finished, ...sequences];
  };
  return parse(false);
}

const inPackage = (directory, packages) =>
  packages.some((name) =>
    name.endsWith("/...")
      ? directory === name.slice(0, -4) ||
        directory.startsWith(name.slice(0, -3))
      : directory === name,
  );

function workflowProblems(workflow, ran) {
  const problems = [];
  // The list only reaches the runners through this matrix and this step.
  if (/^\s*(?:exclude|include):/m.test(workflow))
    problems.push(`${workflowPath} must not exclude or include matrix entries`);
  if (
    !/^\s*- name: .*\n\s*run: make verify-agent-host-\$\{\{ matrix\.lane \}\}\s*$/m.test(
      workflow,
    )
  )
    problems.push(
      `${workflowPath} needs an unconditional step: run: make ${lanePrefix}\${{ matrix.lane }}`,
    );
  const lists = [
    ...workflow.matchAll(/^\s*lane:\s*\[([^\]]*)\]\s*(?:#.*)?$/gm),
  ];
  if (lists.length !== 1) {
    problems.push(
      `${workflowPath} needs exactly one inline lane list, found ${lists.length}`,
    );
    return problems;
  }
  const listed = lists[0][1]
    .split(",")
    .map((name) => name.trim().replace(/^['"]|['"]$/g, ""))
    .filter(Boolean)
    .sort();
  const expected = ran.map((name) => name.replace(lanePrefix, "")).sort();
  if (listed.join() !== expected.join())
    problems.push(
      `${workflowPath} lanes [${listed.join(", ")}] differ from ${aggregate} targets [${expected.join(", ")}]`,
    );
  return problems;
}

// Reads the selection from the Makefile, because `-run` only reaches the
// packages on its own command line: a matching name in another package still
// never runs. `tests` lists every Go test, so a lane may select non-native tests.
export function nativeLaneProblems({ makefile, workflow, tests }) {
  const { recipes, unread, expand } = parseMakefile(makefile);
  const problems = unread.map((line) => `Makefile: cannot read: ${line}`);
  const lanes = [...recipes.keys()].filter((name) =>
    name.startsWith(lanePrefix),
  );

  const ran = [];
  for (const line of recipes.get(aggregate) ?? []) {
    const lane = /^[@+-]*\$\(MAKE\) (\S+)$/.exec(line)?.[1];
    if (lane) ran.push(lane);
    else problems.push(`${aggregate} may only run lanes, found: ${line}`);
  }
  for (const name of ran)
    if (!lanes.includes(name))
      problems.push(`${aggregate} runs ${name}, which is not a lane rule`);
  for (const name of lanes)
    if (!ran.includes(name))
      problems.push(`${name} is not run by ${aggregate}`);
  problems.push(...workflowProblems(workflow, ran));

  const commands = [];
  let refused = false;
  for (const lane of lanes)
    try {
      const found = recipes
        .get(lane)
        .flatMap((line) => goTests(expand(line)))
        .map((command) => ({
          ...command,
          lane,
          branches: alternatives(command.pattern).map((source) => ({
            source,
            expression: new RegExp(source, "u"),
          })),
        }));
      if (!found.length) problems.push(`${lane} runs no go test`);
      commands.push(...found);
    } catch (error) {
      problems.push(`${lane}: ${error.message}`);
      refused = true;
    }
  // What a refused lane selects is unknown, so reporting its tests as unselected
  // would be false.
  if (refused) return problems;

  const selections = new Map();
  for (const { lane, pattern, packages, branches } of commands) {
    const candidates = tests.filter((test) =>
      inPackage(test.directory, packages),
    );
    for (const { source, expression } of branches)
      if (!candidates.some((test) => expression.test(test.name)))
        problems.push(
          `${lane}: -run alternative ${source} matches no test in ${packages.join(", ")}`,
        );
    for (const test of candidates) {
      if (!branches.some(({ expression }) => expression.test(test.name)))
        continue;
      const key = `${test.directory}.${test.name}`;
      selections.set(key, [
        ...(selections.get(key) ?? []),
        `${lane} '${pattern}'`,
      ]);
    }
  }

  for (const { name, directory } of tests) {
    if (!name.startsWith("TestNative")) continue;
    const selected = selections.get(`${directory}.${name}`) ?? [];
    if (helpers.get(name) === directory) {
      if (selected.length)
        problems.push(
          `${name} is a helper that no lane may select, but ${selected.join(" and ")} does`,
        );
    } else if (!selected.length)
      problems.push(
        `${name} in ${directory} is selected by no ${lanePrefix}* -run, so CI never runs it`,
      );
    else if (selected.length > 1)
      problems.push(
        `${name} in ${directory} is selected more than once: ${selected.join(" and ")}`,
      );
  }
  for (const [name, directory] of helpers)
    if (
      !tests.some((test) => test.name === name && test.directory === directory)
    )
      problems.push(`helper exception ${name} has no test in ${directory}`);
  return problems;
}

// The tests a lane's race-enabled `go test` can see on this architecture. Asking
// Go applies its build constraints and ignored paths, wherever this check runs.
function linuxTests(cwd, architecture) {
  const packages = execFileSync(
    "go",
    [
      "list",
      "-e",
      "-tags",
      "race",
      "-f",
      '{{.Dir}}\t{{.Root}}\t{{join .TestGoFiles "\t"}}\t{{join .XTestGoFiles "\t"}}',
      "./...",
    ],
    {
      cwd: join(cwd, root),
      encoding: "utf8",
      env: {
        ...process.env,
        GOOS: "linux",
        GOARCH: architecture,
        CGO_ENABLED: "1",
      },
    },
  );
  const tests = [];
  for (const line of packages.split("\n").filter(Boolean)) {
    const [directory, moduleRoot, ...files] = line.split("\t");
    for (const file of files.filter(Boolean))
      for (const [, name] of readFileSync(
        join(directory, file),
        "utf8",
      ).matchAll(/^func (Test[^\s(]*)\(/gm))
        tests.push({
          name,
          directory: posix.join(root, relative(moduleRoot, directory)),
        });
  }
  return tests;
}

function run() {
  const cwd = process.cwd();
  const makefile = readFileSync(join(cwd, "Makefile"), "utf8");
  const workflow = readFileSync(join(cwd, workflowPath), "utf8");
  const problems = new Map();
  let native = 0;
  for (const architecture of architectures) {
    const tests = linuxTests(cwd, architecture);
    native = tests.filter((test) => test.name.startsWith("TestNative")).length;
    for (const problem of nativeLaneProblems({ makefile, workflow, tests }))
      problems.set(problem, [...(problems.get(problem) ?? []), architecture]);
  }
  if (problems.size)
    throw new Error(
      [...problems]
        .map(([problem, affected]) =>
          affected.length === architectures.length
            ? problem
            : `${problem} (linux/${affected.join(", linux/")} only)`,
        )
        .join("\n"),
    );
  console.log(
    `Native lanes select each native test once (${native - helpers.size} selected, ${helpers.size} helpers).`,
  );
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
