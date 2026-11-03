import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { devNull, tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { ciPlan } from "./ci-plan.mjs";

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
  return { cwd, git, write, commit, merge };
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
    [["docs/hosting.md"], []],
    [["apps/platform/src/screen.tsx"], ["browser"]],
    [["services/platform/internal/auth/service.go"], ["go", "browser"]],
    [["services/platform/cmd/cli/main.go"], ["go", "browser"]],
    [
      ["services/platform/internal/agent/engine.go"],
      ["native", "go", "browser"],
    ],
    [
      ["services/platform/internal/httpapi/execution.go"],
      ["native", "go", "browser"],
    ],
    [
      ["experiments/durable-execution/verify.sh"],
      ["native", "go", "browser", "durable"],
    ],
    [
      ["experiments/git-compose/providers_test.go"],
      ["native", "go", "browser", "git"],
    ],
    [
      ["experiments/canvas-editor/src/app.tsx"],
      ["native", "go", "browser", "canvas"],
    ],
    [["pnpm-lock.yaml"], ["native", "go", "browser", "canvas"]],
    [["Makefile"], ["native", "go", "browser", "durable", "git", "canvas"]],
    [
      ["scripts/ci-plan.mjs"],
      ["native", "go", "browser", "durable", "git", "canvas"],
    ],
    [["AGENTS.md"], ["native", "go", "browser"]],
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
  assert.throws(() => ciPlan("merge_group", repo.cwd), /Unsupported/);
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
