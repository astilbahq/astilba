import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { nativePlan } from "./ci-native-plan.mjs";

function repository(t) {
  const cwd = mkdtempSync(join(tmpdir(), "astilba-ci-selection-"));
  t.after(() => rmSync(cwd, { recursive: true, force: true }));
  const git = (...args) =>
    execFileSync("git", args, {
      cwd,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
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
      assert.equal(nativePlan("pull_request", repo.cwd), expected);
    });
  }
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
      assert.equal(nativePlan("pull_request", repo.cwd), true);
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
  assert.equal(nativePlan("pull_request", repo.cwd), false);
});

test("main pushes and manual runs require native checks without consulting a diff", () => {
  assert.equal(nativePlan("push", "/nonexistent"), true);
  assert.equal(nativePlan("workflow_dispatch", "/nonexistent"), true);
});

test("missing or unexpected PR metadata fails instead of reporting a skip", (t) => {
  const repo = repository(t);
  assert.throws(() => nativePlan("pull_request", repo.cwd), /two-parent/);
  repo.write("apps/platform/src/ui.tsx");
  repo.merge();
  const shallow = join(repo.cwd, "shallow");
  repo.git("clone", "--depth=1", `file://${repo.cwd}`, shallow);
  assert.throws(() => nativePlan("pull_request", shallow), /two-parent/);
  assert.throws(() => nativePlan("merge_group", repo.cwd), /Unsupported/);
});
