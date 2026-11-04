import { lstatSync, readdirSync, readFileSync } from "node:fs";
import { join, posix } from "node:path";
import { pathToFileURL } from "node:url";

const root = "services/platform";
const workflowPath = ".github/workflows/ci.yml";
const aggregate = "verify-agent-host";
const lanePrefix = `${aggregate}-`;
// Re-executed inside an isolated host process or network namespace by the test
// that needs them; run directly they only skip.
const helpers = new Map([
  ["TestNativeCrossHostHelper", "services/platform/internal/agent"],
  ["TestNativeEndpointHelper", "services/platform/internal/hostnetwork"],
]);

function parseMakefile(text) {
  const variables = new Map([["MAKE", "make"]]);
  const rules = new Map();
  let current;
  for (const line of text.replace(/\\\n\s*/g, " ").split("\n")) {
    if (line.startsWith("\t")) {
      current?.recipe.push(line.trim());
      continue;
    }
    const content = line.replace(/#.*/, "");
    const variable = /^\s*([\w.-]+)\s*(?:::?|\?)?=\s*(.*?)\s*$/.exec(content);
    const rule = /^([^\s:=]+(?:\s+[^\s:=]+)*)\s*:(?!=)\s*(.*?)\s*$/.exec(
      content,
    );
    if (variable) variables.set(variable[1], variable[2]);
    else if (rule) {
      current = {
        prerequisites: rule[2].split(/\s+/).filter(Boolean),
        recipe: [],
      };
      for (const target of rule[1].split(/\s+/)) rules.set(target, current);
    }
  }
  // An unresolvable reference throws, because guessing could hide a selection.
  const expand = (value) =>
    value.replace(
      /\$(?:\$|\(([^()]*)\)|\{([^{}]*)\})/g,
      (_, parenthesised, braced) => {
        const name = parenthesised ?? braced;
        if (name === undefined) return "$";
        if (!variables.has(name)) throw new Error(`cannot resolve $(${name})`);
        return expand(variables.get(name));
      },
    );
  return { rules, expand };
}

// The `go test` commands of one expanded recipe line, with the packages resolved
// against any `cd` that precedes them on that line, as each line is its own shell.
function goTests(line) {
  const words = line
    .replace(/^[@+-]+/, "")
    .match(/(?:'[^']*'|"[^"]*"|[^\s'"])+/g);
  const commands = [[]];
  for (const word of words ?? [])
    if (word === "&&") commands.push([]);
    else commands.at(-1).push(word.replace(/'([^']*)'|"([^"]*)"/g, "$1$2"));
  let directory = ".";
  const found = [];
  for (const command of commands) {
    if (command[0] === "cd") directory = posix.join(directory, command[1]);
    const go = command.findIndex(
      (word, i) => /(^|\/)go$/.test(word) && command[i + 1] === "test",
    );
    if (go < 0) continue;
    const runs = [];
    const packages = [];
    const args = command.slice(go + 2);
    for (let i = 0; i < args.length; i++) {
      const flag = /^--?(?:test\.)?(run|skip)(?:=(.*))?$/.exec(args[i]);
      if (flag?.[1] === "skip") throw new Error("-skip hides selected tests");
      if (flag) runs.push(flag[2] ?? args[++i]);
      else if (args[i].startsWith("./"))
        packages.push(posix.join(directory, args[i]));
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

// Reads the selection from the Makefile, because `-run` only reaches the
// packages on its own command line: a matching name in another package still
// never runs. `tests` lists every Go test, so a lane may select non-native tests.
export function nativeLaneProblems({ makefile, workflow, tests }) {
  const problems = [];
  const { rules, expand } = parseMakefile(makefile);
  const lanes = [...rules.keys()].filter((name) => name.startsWith(lanePrefix));

  const ran = [...(rules.get(aggregate)?.prerequisites ?? [])];
  if (!rules.has(aggregate)) problems.push(`Makefile has no ${aggregate} rule`);
  for (const line of rules.get(aggregate)?.recipe ?? []) {
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

  const lists = [
    ...workflow.matchAll(/^\s*lane:\s*\[([^\]]*)\]\s*(?:#.*)?$/gm),
  ];
  if (lists.length !== 1)
    problems.push(
      `${workflowPath} needs exactly one inline lane list, found ${lists.length}`,
    );
  else {
    const listed = lists[0][1]
      .split(",")
      .map((name) => name.trim().replace(/^['"]|['"]$/g, ""))
      .filter(Boolean)
      .sort();
    const expected = ran
      .filter((name) => name.startsWith(lanePrefix))
      .map((name) => name.slice(lanePrefix.length))
      .sort();
    if (listed.join() !== expected.join())
      problems.push(
        `${workflowPath} lanes [${listed.join(", ")}] differ from ${aggregate} targets [${expected.join(", ")}]`,
      );
  }

  const selections = new Map();
  for (const lane of lanes) {
    let commands;
    let branches;
    try {
      commands = rules
        .get(lane)
        .recipe.flatMap((line) => goTests(expand(line)));
      branches = commands.map(({ pattern }) =>
        alternatives(pattern).map((source) => ({
          source,
          expression: new RegExp(source, "u"),
        })),
      );
    } catch (error) {
      problems.push(`${lane}: ${error.message}`);
      continue;
    }
    if (!commands.length) problems.push(`${lane} runs no go test`);
    commands.forEach(({ pattern, packages }, index) => {
      const candidates = tests.filter((test) =>
        inPackage(test.directory, packages),
      );
      for (const { source, expression } of branches[index])
        if (!candidates.some((test) => expression.test(test.name)))
          problems.push(
            `${lane}: -run alternative ${source} matches no test in ${packages.join(", ")}`,
          );
      for (const test of candidates) {
        if (!branches[index].some((b) => b.expression.test(test.name)))
          continue;
        const key = `${test.directory}.${test.name}`;
        selections.set(key, [
          ...(selections.get(key) ?? []),
          `${lane} '${pattern}'`,
        ]);
      }
    });
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

// Reads source rather than `go test -list`, which would need Linux to see the
// linux-tagged native tests. Skips the paths the Go tool itself ignores.
function testFunctions(directory, prefix, found = []) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    if (/^[._]/.test(entry.name)) continue;
    const path = `${prefix}/${entry.name}`;
    if (entry.isSymbolicLink())
      throw new Error(`Native lane checks refuse symlinks: ${path}`);
    if (entry.isDirectory()) {
      if (entry.name !== "testdata" && entry.name !== "vendor")
        testFunctions(join(directory, entry.name), path, found);
    } else if (entry.isFile() && entry.name.endsWith("_test.go"))
      for (const [, name] of readFileSync(
        join(directory, entry.name),
        "utf8",
      ).matchAll(/^func (Test[^\s(]*)\(/gm))
        found.push({ name, directory: prefix });
  }
  return found;
}

function run() {
  const cwd = process.cwd();
  lstatSync(join(cwd, root));
  const tests = testFunctions(join(cwd, root), root);
  const problems = nativeLaneProblems({
    makefile: readFileSync(join(cwd, "Makefile"), "utf8"),
    workflow: readFileSync(join(cwd, workflowPath), "utf8"),
    tests,
  });
  if (problems.length) throw new Error(problems.join("\n"));
  const native = tests.filter((test) => test.name.startsWith("TestNative"));
  console.log(
    `Native lanes select each native test once (${native.length - helpers.size} selected, ${helpers.size} helpers).`,
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
