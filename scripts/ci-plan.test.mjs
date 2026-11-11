import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import {
  mkdtempSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  realpathSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { devNull, tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { ciPlan } from "./ci-plan.mjs";

const root = fileURLToPath(new URL("..", import.meta.url));
const workflows = join(root, ".github/workflows");

const gitEnvironment = {
  ...Object.fromEntries(
    Object.entries(process.env).filter(([name]) => !name.startsWith("GIT_")),
  ),
  GIT_CONFIG_NOSYSTEM: "1",
  GIT_CONFIG_GLOBAL: devNull,
};

function repository(t) {
  const cwd = mkdtempSync(join(tmpdir(), "astilba-ci-selection-"));
  t.after(() => rmSync(cwd, { recursive: true, force: true }));
  const git = (...args) =>
    execFileSync("git", args, {
      cwd,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
      env: gitEnvironment,
    });
  git("init", "--initial-branch=main");
  git("config", "user.name", "CI selection fixture");
  git("config", "user.email", "ci@example.invalid");
  const write = (path, content = "fixture\n") => {
    mkdirSync(dirname(join(cwd, path)), { recursive: true });
    writeFileSync(join(cwd, path), content);
  };
  const commit = () => {
    git("add", "-A");
    git("commit", "-m", "fixture");
  };
  write("README.md");
  write("services/platform/native.go");
  commit();
  git("switch", "-c", "feature");
  const merge = () => {
    commit();
    git("switch", "main");
    git("merge", "--no-ff", "feature", "-m", "tested merge");
  };
  // Stacks one single-parent squash commit per pull request and returns the base.
  const mergeGroup = (...pullRequests) => {
    git("switch", "main");
    const base = git("rev-parse", "HEAD").trim();
    for (const paths of pullRequests) {
      for (const path of paths) write(path, "changed\n");
      commit();
    }
    return base;
  };
  return { cwd, git, write, commit, merge, mergeGroup };
}

test("only known frontend and documentation changes skip native checks", async (t) => {
  for (const [paths, expected] of [
    [
      [
        "apps/platform/src/screen.tsx",
        "apps/platform/theme/tokens.ts",
        "docs/design.md",
      ],
      false,
    ],
    [
      [
        "apps/platform/tests/a.spec.ts",
        "apps/platform/panda.config.ts",
        "README.md",
      ],
      false,
    ],
    [["services/platform/native.go"], true],
    [["contracts/platform/openapi.yaml"], true],
    [["pnpm-lock.yaml"], true],
    [["apps/platform/package.json"], true],
    [[".github/workflows/ci.yml"], true],
    [["scripts/platform-build.mjs"], true],
    [["unknown/input"], true],
    [["apps/platform/src/AGENTS.md"], true],
    [["docs/REVIEW.md"], true],
    [["apps/platform/src/ui.tsx", "services/platform/runtime.go"], true],
    [["apps/platform/src/line\nbreak.tsx"], false],
  ]) {
    await t.test(paths.join(", "), (t) => {
      const repo = repository(t);
      for (const path of paths) repo.write(path, "changed\n");
      repo.merge();
      assert.equal(ciPlan("pull_request", repo.cwd).native, expected);
    });
  }
});

test("each suite runs only for paths that can affect it", async (t) => {
  const cases = [
    [["docs/agent.md"], []],
    [["docs/hosting.md"], ["native"]],
    [["apps/platform/src/screen.tsx"], ["browser"]],
    [
      ["services/platform/internal/auth/service.go"],
      ["go", "browser", "vulnerabilities"],
    ],
    [
      ["services/platform/cmd/cli/main.go"],
      ["go", "browser", "vulnerabilities"],
    ],
    [
      ["services/platform/internal/agent/engine.go"],
      ["native", "go", "browser", "vulnerabilities"],
    ],
    [
      ["services/platform/internal/httpapi/execution.go"],
      ["native", "go", "browser", "vulnerabilities"],
    ],
    [
      ["experiments/durable-execution/verify.sh"],
      ["native", "go", "browser", "durable"],
    ],
    [
      ["experiments/git-compose/providers_test.go"],
      ["native", "go", "browser", "git", "vulnerabilities"],
    ],
    [
      ["experiments/canvas-editor/src/app.tsx"],
      ["native", "go", "browser", "canvas"],
    ],
    [
      ["experiments/canvas-editor/package.json"],
      ["native", "go", "browser", "canvas", "vulnerabilities"],
    ],
    [
      ["pnpm-lock.yaml"],
      ["native", "go", "browser", "canvas", "vulnerabilities"],
    ],
    [
      ["services/platform/go.sum"],
      ["native", "go", "browser", "vulnerabilities"],
    ],
    [
      ["apps/platform/package.json"],
      ["native", "go", "browser", "vulnerabilities"],
    ],
    [
      ["services/platform/internal/publicsuffix/public_suffix_list.dat"],
      ["native", "go", "browser", "vulnerabilities"],
    ],
    [
      [".github/vulnerability-allowlist.json"],
      ["native", "go", "browser", "vulnerabilities"],
    ],
    [
      ["Makefile"],
      [
        "native",
        "go",
        "browser",
        "durable",
        "git",
        "canvas",
        "vulnerabilities",
      ],
    ],
    [
      ["scripts/ci-plan.mjs"],
      [
        "native",
        "go",
        "browser",
        "durable",
        "git",
        "canvas",
        "vulnerabilities",
      ],
    ],
    [["AGENTS.md"], ["native", "go", "browser"]],
    [
      [".github/actions/install-pnpm/action.yml"],
      ["native", "go", "browser", "canvas", "vulnerabilities"],
    ],
    [
      ["scripts/install-pnpm.mjs"],
      ["native", "go", "browser", "canvas", "vulnerabilities"],
    ],
    [
      [".github/actions/pull-images/action.yml"],
      ["native", "go", "browser", "durable", "git"],
    ],
    [
      ["scripts/fetch-attempts.mjs"],
      [
        "native",
        "go",
        "browser",
        "durable",
        "git",
        "canvas",
        "vulnerabilities",
      ],
    ],
  ];
  for (const [paths, selected] of cases)
    await t.test(paths.join(", "), (t) => {
      const repo = repository(t);
      for (const path of paths) repo.write(path, "changed\n");
      repo.merge();
      const plan = ciPlan("pull_request", repo.cwd);
      assert.deepEqual(
        Object.keys(plan).filter((suite) => plan[suite]),
        selected,
      );
    });
});

test("a native deletion or rename into a frontend directory still runs native checks", async (t) => {
  for (const rename of [false, true])
    await t.test(String(rename), (t) => {
      const repo = repository(t);
      if (rename) {
        mkdirSync(join(repo.cwd, "apps/platform/src"), { recursive: true });
        repo.git(
          "mv",
          "services/platform/native.go",
          "apps/platform/src/moved.go",
        );
      } else repo.git("rm", "services/platform/native.go");
      repo.merge();
      assert.equal(ciPlan("pull_request", repo.cwd).native, true);
    });
});

test("base-only native changes do not turn a UI-only tested merge into a native PR", (t) => {
  const repo = repository(t);
  repo.write("apps/platform/src/ui.tsx");
  repo.commit();
  repo.git("switch", "main");
  repo.write("services/platform/base-only.go");
  repo.commit();
  repo.git("merge", "--no-ff", "feature", "-m", "tested merge");
  assert.equal(ciPlan("pull_request", repo.cwd).native, false);
});

test("main pushes, manual and scheduled runs select every suite without consulting a diff", () => {
  for (const event of ["push", "workflow_dispatch", "schedule"])
    assert.ok(
      Object.values(ciPlan(event, "/nonexistent")).every(Boolean),
      event,
    );
});

test("missing or unexpected PR metadata fails instead of reporting a skip", (t) => {
  const repo = repository(t);
  assert.throws(() => ciPlan("pull_request", repo.cwd), /two-parent/);
  repo.write("apps/platform/src/ui.tsx");
  repo.merge();
  const shallow = join(repo.cwd, "shallow");
  repo.git("clone", "--depth=1", `file://${repo.cwd}`, shallow);
  assert.throws(() => ciPlan("pull_request", shallow), /two-parent/);
  assert.throws(() => ciPlan("pull_request_target", repo.cwd), /Unsupported/);
  for (const missing of [undefined, ""])
    assert.throws(() => ciPlan("merge_group", repo.cwd, missing));
});

test("a merge group's suites cover every pull request since its base", (t) => {
  const repo = repository(t);
  const base = repo.mergeGroup(
    ["services/platform/native.go"],
    ["apps/platform/src/ui.tsx"],
  );
  assert.equal(ciPlan("merge_group", repo.cwd, base).native, true);
  const parent = repo.git("rev-parse", "HEAD^1").trim();
  assert.equal(ciPlan("merge_group", repo.cwd, parent).native, false);
});

test("an empty merge group selects every suite", (t) => {
  const repo = repository(t);
  const head = repo.mergeGroup();
  assert.ok(
    Object.values(ciPlan("merge_group", repo.cwd, head)).every(Boolean),
  );
});

const { contexts } = JSON.parse(
  readFileSync(join(root, ".github/required-status-checks.json"), "utf8"),
);
const workflow = (file) => readFileSync(join(workflows, file), "utf8");
const reports = (file) =>
  contexts.filter((name) => workflow(file).includes(`    name: ${name}\n`));
const gateWorkflows = readdirSync(workflows).filter(
  (file) => reports(file).length,
);

test("every required check reports on merge groups", () => {
  for (const file of gateWorkflows)
    assert.match(workflow(file), /^ {2}merge_group:$/m, file);
  assert.deepEqual(
    gateWorkflows.flatMap(reports).toSorted(),
    contexts.toSorted(),
  );
});

function concurrency(file, github) {
  const setting = (name) =>
    new RegExp(`^  ${name}: (.*)$`, "m")
      .exec(workflow(file))[1]
      .replace(/\$\{\{(.*?)\}\}/g, (_, expression) =>
        new Function("github", `return ${expression.replaceAll("==", "===")}`)({
          workflow: file,
          ...github,
        }),
      );
  return {
    group: setting("group"),
    cancels: setting("cancel-in-progress") === "true",
  };
}

// A displaced main run fails no required check, so nothing else reports it.
test("only a pull request's newer commit replaces a run", () => {
  for (const file of gateWorkflows) {
    for (const event_name of ["push", "workflow_dispatch", "schedule"]) {
      const run = (run_id) =>
        concurrency(file, { event_name, ref: "refs/heads/main", run_id });
      assert.notEqual(run("1").group, run("2").group, `${file} ${event_name}`);
    }
    const pull = (run_id) =>
      concurrency(file, {
        event_name: "pull_request",
        ref: "refs/pull/1/merge",
        run_id,
      });
    assert.equal(pull("1").group, pull("2").group, file);
    assert.equal(pull("2").cancels, true, file);
    const queued = concurrency(file, {
      event_name: "merge_group",
      ref: "refs/heads/gh-readonly-queue/main/pr-1-abc",
      run_id: "2",
    });
    assert.equal(queued.cancels, false, file);
  }
});

function planStep(file) {
  const lines = workflow(file).split("\n");
  const start = lines.indexOf(
    "        run: |",
    lines.findIndex((line) => line.endsWith("id: plan")),
  );
  const end = lines.findIndex(
    (line, index) => index > start && line && !line.startsWith("          "),
  );
  const key = /'\^(\w+)='/.exec(lines.slice(start, end).join("\n"))[1];
  return { key, script: lines.slice(start + 1, end).join("\n") };
}

const suites = Object.keys(ciPlan("push"));
const hostilePlanner = `import { appendFileSync } from "node:fs";
for (const suite of ${JSON.stringify(suites)})
  appendFileSync(process.env.GITHUB_OUTPUT, suite + "=false\\n");
`;

function runPlanStep(t, repo, { key, script }, event, base) {
  const temp = realpathSync(mkdtempSync(join(tmpdir(), "astilba-ci-runner-")));
  t.after(() => rmSync(temp, { recursive: true, force: true }));
  const output = join(temp, "output");
  writeFileSync(output, "");
  execFileSync("bash", ["-e", "-c", script], {
    cwd: repo.cwd,
    stdio: "ignore",
    env: {
      ...gitEnvironment,
      GITHUB_EVENT_NAME: event,
      GITHUB_OUTPUT: output,
      RUNNER_TEMP: temp,
      MERGE_GROUP_BASE_SHA: base ?? "",
    },
  });
  return new RegExp(`^${key}=(\\w+)$`, "m").exec(
    readFileSync(output, "utf8"),
  )[1];
}

test("each workflow runs the base's planner, not the tested commit's", async (t) => {
  const steps = gateWorkflows.map(planStep);
  for (const { key, script } of steps) {
    assert.ok(suites.includes(key), key);
    assert.equal(
      script.replaceAll(`'^${key}='`, "KEY").replaceAll(`"${key}=true"`, "KEY"),
      steps[0].script
        .replaceAll(`'^${steps[0].key}='`, "KEY")
        .replaceAll(`"${steps[0].key}=true"`, "KEY"),
    );
  }
  const step = steps[0];
  const planner = readFileSync(join(root, "scripts/ci-plan.mjs"), "utf8");
  const fixture = (t) => {
    const repo = repository(t);
    repo.git("switch", "main");
    repo.write("scripts/ci-plan.mjs", planner);
    repo.commit();
    repo.git("switch", "feature");
    repo.git("merge", "--ff-only", "main");
    return repo;
  };
  await t.test("a hostile planner selects nothing it controls", (t) => {
    const repo = fixture(t);
    const base = repo.mergeGroup();
    repo.write("scripts/ci-plan.mjs", hostilePlanner);
    repo.commit();
    assert.equal(runPlanStep(t, repo, step, "merge_group", base), "true");

    const pull = fixture(t);
    pull.write("scripts/ci-plan.mjs", hostilePlanner);
    pull.merge();
    assert.equal(runPlanStep(t, pull, step, "pull_request"), "true");
  });
  await t.test("the base planner's skip is honoured", (t) => {
    const repo = fixture(t);
    const base = repo.mergeGroup(["docs/agent.md"]);
    assert.equal(runPlanStep(t, repo, step, "merge_group", base), "false");

    const pull = fixture(t);
    pull.write("docs/agent.md");
    pull.merge();
    assert.equal(runPlanStep(t, pull, step, "pull_request"), "false");
  });
  await t.test("no base planner selects the suite", (t) => {
    const repo = repository(t);
    const base = repo.mergeGroup(["docs/agent.md"]);
    assert.equal(runPlanStep(t, repo, step, "merge_group", base), "true");
    assert.equal(runPlanStep(t, repo, step, "merge_group"), "true");
    assert.equal(runPlanStep(t, repo, step, "push"), "true");
  });
});

test("inherited Git selectors cannot redirect fixture writes or planner reads", (t) => {
  const foreign = repository(t);
  foreign.write("sentinel.txt", "pending work\n");
  const head = foreign.git("rev-parse", "HEAD");
  const status = foreign.git("status", "--porcelain=v1");
  const configuration = foreign.git("config", "--local", "--list");
  const result = spawnSync(
    process.execPath,
    [
      "--test",
      "--test-reporter=tap",
      "--test-name-pattern=^only known frontend and documentation changes",
      fileURLToPath(import.meta.url),
    ],
    {
      encoding: "utf8",
      env: {
        ...Object.fromEntries(
          Object.entries(process.env).filter(
            ([name]) => !name.startsWith("NODE_TEST_"),
          ),
        ),
        GIT_DIR: join(foreign.cwd, ".git"),
        GIT_WORK_TREE: foreign.cwd,
        GIT_INDEX_FILE: join(foreign.cwd, ".git/index"),
      },
    },
  );
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.match(
    result.stdout,
    /^ok \d+ - only known frontend and documentation changes/m,
  );
  assert.equal(foreign.git("rev-parse", "HEAD"), head);
  assert.equal(foreign.git("status", "--porcelain=v1"), status);
  assert.equal(foreign.git("config", "--local", "--list"), configuration);
});
