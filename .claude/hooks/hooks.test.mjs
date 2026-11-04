import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import {
  cpSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { test } from "node:test";
import { checksFor, formattable } from "./checks.mjs";

const repository = resolve(import.meta.dirname, "../..");

// Real paths, so moving one of these directories fails here rather than quietly
// leaving it unchecked.
test("changed paths select the checks CI would fail", () => {
  const selected = {
    "services/platform/go.mod": ["go vet services/platform", "generated files"],
    "services/platform/internal/auth/password.go": [
      "gofmt",
      "source size",
      "inline SQL",
      "go vet services/platform",
    ],
    "services/platform/db/queries/auth.sql": ["generated files"],
    "contracts/platform/openapi.yaml": ["generated files"],
    "apps/platform/src/main.tsx": ["format", "source size", "dashboard"],
    "experiments/canvas-editor/src/App.tsx": ["format", "canvas"],
    "experiments/git-compose/go.mod": ["go vet experiments/git-compose"],
    "scripts/source-size-baseline.json": ["source size"],
    ".github/workflows/ci.yml": ["CI plan"],
    ".claude/hooks/stop.mjs": ["hooks"],
  };
  for (const [path, names] of Object.entries(selected)) {
    assert.ok(existsSync(join(repository, path)), `${path} no longer exists`);
    const checks = checksFor(repository, [path]);
    for (const name of names)
      assert.ok(
        checks.some((check) => check.name === name),
        `${path} should select ${name}`,
      );
  }
  assert.deepEqual(checksFor(repository, ["README.md", "docs/agent.md"]), []);
  assert.ok(formattable("vite.config.ts"));
});

test("settings run each hook through the Node selector", () => {
  const { hooks } = JSON.parse(
    readFileSync(join(repository, ".claude/settings.json"), "utf8"),
  );
  const script = (event) => {
    const [, name] = hooks[event][0].hooks[0].command.match(
      /\.claude\/hooks\/node\.sh" (\S+)$/,
    );
    assert.ok(existsSync(join(import.meta.dirname, name)), name);
  };
  ["SessionStart", "PostToolUse", "Stop"].forEach(script);
  const tools = hooks.PostToolUse[0].matcher.split("|");
  assert.ok(tools.includes("Write") && tools.includes("Edit"));
  assert.ok(hooks.Stop[0].hooks[0].timeout <= 120);
});

// Runs the real scripts against a scratch repository whose Makefile decides
// whether the source-size check passes.
function scratch(t) {
  const parent = realpathSync(mkdtempSync(join(tmpdir(), "astilba-hooks-")));
  t.after(() => rmSync(parent, { recursive: true, force: true }));
  const root = join(parent, "repo");
  mkdirSync(join(root, "scripts"), { recursive: true });
  cpSync(import.meta.dirname, join(root, ".claude/hooks"), { recursive: true });
  writeFileSync(
    join(root, "Makefile"),
    "check-source-size:\n\t@cat outcome\n\t@grep -q pass outcome\n",
  );
  writeFileSync(join(root, "scripts/.keep"), "");
  writeFileSync(join(root, ".gitignore"), "outcome\n");
  writeFileSync(join(root, "outcome"), "pass\n");
  const git = (...args) =>
    execFileSync(
      "git",
      ["-c", "user.name=test", "-c", "user.email=test@example.com", ...args],
      { cwd: root, encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] },
    );
  git("init", "--quiet");
  git("add", ".");
  git("commit", "--quiet", "--message", "initial");
  const hook = (script, input, cwd = root) => {
    const output = execFileSync(
      process.execPath,
      [join(cwd, ".claude/hooks", script)],
      {
        cwd,
        encoding: "utf8",
        input: JSON.stringify({ session_id: "s", cwd, ...input }),
      },
    );
    return output ? JSON.parse(output) : null;
  };
  return {
    root,
    git,
    hook,
    start: (cwd) => hook("session-start.mjs", {}, cwd),
    stop: (input, cwd) => hook("stop.mjs", input, cwd),
    write: (path, text = "x\n", base = root) =>
      writeFileSync(join(base, path), text),
    outcome: (text) => writeFileSync(join(root, "outcome"), `${text}\n`),
  };
}

test("Bash writes and commits are detected, earlier changes are not", (t) => {
  const repo = scratch(t);
  repo.outcome("fail");
  repo.write("scripts/earlier.mjs");
  repo.start();
  assert.equal(repo.stop({}), null);
  repo.write("scripts/bash.mjs");
  assert.equal(repo.stop({}).decision, "block");

  // A file committed before any hook saw it dirty, leaving a clean tree.
  const session_id = "committing";
  repo.hook("session-start.mjs", { session_id });
  repo.write("scripts/committed.mjs");
  repo.git("add", "scripts/committed.mjs");
  repo.git("commit", "--quiet", "--message", "work");
  const { reason } = repo.hook("stop.mjs", { session_id });
  assert.match(reason, /source size/);
});

test("a linked worktree keeps its own session state", (t) => {
  const repo = scratch(t);
  const linked = join(repo.root, "..", "linked");
  repo.git("worktree", "add", "--quiet", "-b", "linked", linked);
  repo.write("Makefile", "check-source-size:\n\t@false\n", linked);
  repo.start(linked);
  repo.write("scripts/work.mjs", "x\n", linked);
  assert.equal(repo.stop({ prompt_id: "1" }, linked).decision, "block");
  assert.equal(repo.stop({ prompt_id: "1" }), null);
  assert.equal(repo.git("status", "--porcelain"), "");
});

test("a failure that cannot be fixed cannot hold the session forever", (t) => {
  const repo = scratch(t);
  repo.outcome("fail");
  repo.start();
  repo.write("scripts/work.mjs");
  const first = repo.stop({ prompt_id: "1" });
  assert.equal(first.decision, "block");
  assert.match(first.reason, /source size[\s\S]*fail/);

  // No change since the block: release at once, then stay quiet.
  assert.equal(repo.stop({ prompt_id: "1" }).decision, undefined);
  assert.equal(repo.stop({ prompt_id: "1" }), null);

  // New attempts keep changing the file: released after the cap, even if the
  // harness reports every continuation under a new prompt.
  const decisions = [];
  for (let attempt = 0; attempt < 6; attempt++) {
    repo.write("scripts/work.mjs", `${attempt}\n`);
    decisions.push(
      repo.stop({ prompt_id: `${attempt}`, stop_hook_active: attempt > 0 })
        ?.decision,
    );
  }
  assert.deepEqual(decisions.slice(0, 4), [
    "block",
    "block",
    "block",
    undefined,
  ]);

  // Settled, but a later prompt with new changes is checked again.
  repo.write("scripts/work.mjs", "later\n");
  assert.equal(repo.stop({ prompt_id: "later" }).decision, "block");
  repo.outcome("pass");
  repo.write("scripts/work.mjs", "fixed\n");
  assert.equal(repo.stop({ prompt_id: "later" }), null);
});

test("a check that cannot run is reported, never counted silently", (t) => {
  const repo = scratch(t);
  repo.write(
    "Makefile",
    "check-source-size:\n\t@echo module lookup disabled by GOPROXY=off\n\t@false\n",
  );
  repo.git("commit", "--quiet", "--all", "--message", "offline");
  repo.start();
  repo.write("scripts/work.mjs");
  assert.match(repo.stop({}).systemMessage, /not run: source size/);
});

test("subagents that may still edit defer the checks", (t) => {
  const repo = scratch(t);
  repo.outcome("fail");
  repo.start();
  repo.write("scripts/work.mjs");
  const background_tasks = [{ id: "1", type: "subagent" }];
  assert.equal(repo.stop({ background_tasks }), null);
  assert.equal(repo.stop({ background_tasks: [] }).decision, "block");
});

test("an edit is recorded and formatted, even with the file gitignored", (t) => {
  const repo = scratch(t);
  repo.outcome("fail");
  repo.write(".gitignore", "outcome\nscripts/ignored.mjs\nignored.go\n");
  repo.git("commit", "--quiet", "--all", "--message", "ignore");
  repo.start();
  repo.write("scripts/ignored.mjs");
  assert.equal(repo.stop({ prompt_id: "1" }), null);
  const edit = (path) =>
    repo.hook("after-edit.mjs", {
      tool_input: { file_path: join(repo.root, path) },
    });
  assert.equal(edit("scripts/ignored.mjs"), null);
  assert.equal(repo.stop({ prompt_id: "1" }).decision, "block");

  repo.write("ignored.go", "package   x\n");
  edit("ignored.go");
  assert.equal(
    readFileSync(join(repo.root, "ignored.go"), "utf8"),
    "package x\n",
  );
  const outside = join(repo.root, "..", "outside.go");
  writeFileSync(outside, "package   x\n");
  assert.equal(
    repo.hook("after-edit.mjs", { tool_input: { file_path: outside } }),
    null,
  );
  assert.equal(readFileSync(outside, "utf8"), "package   x\n");
});
