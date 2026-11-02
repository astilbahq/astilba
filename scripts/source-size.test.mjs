import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import {
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { devNull, tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const script = fileURLToPath(new URL("./source-size.mjs", import.meta.url));
const baseline = "scripts/source-size-baseline.json";
const content = (count) => "line\n".repeat(count);
const gitEnvironment = {
  ...Object.fromEntries(
    Object.entries(process.env).filter(([name]) => !name.startsWith("GIT_")),
  ),
  GIT_CONFIG_NOSYSTEM: "1",
  GIT_CONFIG_GLOBAL: devNull,
};

function fixture(t, files = {}, withBaseline = true) {
  const root = mkdtempSync(join(tmpdir(), "astilba-source-size-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const write = (path, text) => {
    mkdirSync(dirname(join(root, path)), { recursive: true });
    writeFileSync(join(root, path), text);
  };
  const git = (...args) =>
    execFileSync("git", args, {
      cwd: root,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
      env: gitEnvironment,
    }).trim();
  const commit = () => {
    git("add", "-A");
    git("commit", "-m", "Source-size fixture");
    return git("rev-parse", "HEAD");
  };
  const run = (args = [], env = {}, cwd = root) =>
    spawnSync(process.execPath, [script, ...args], {
      cwd,
      encoding: "utf8",
      env: { ...process.env, CI: "", SOURCE_SIZE_BASE: "", ...env },
    });
  git("init", "--initial-branch=main");
  git("config", "user.name", "Source-size fixture");
  git("config", "user.email", "fixture@example.invalid");
  mkdirSync(join(root, "scripts"));
  write("README.md", "fixture\n");
  if (withBaseline) write(baseline, "{}\n");
  for (const [path, text] of Object.entries(files)) write(path, text);
  const base = commit();
  return { root, write, git, commit, run, base };
}

function pass(result) {
  assert.equal(result.status, 0, result.stderr || result.error?.message);
}
function fail(result, message) {
  assert.equal(result.status, 1, result.stdout);
  assert.match(result.stderr, message);
}

test("source and test caps count physical lines without an extra terminal line", (t) => {
  const f = fixture(t);
  f.write("services/platform/boundary.go", content(600));
  f.write("apps/platform/a.spec.ts", content(800));
  f.write("apps/platform/tests/helper.ts", content(800));
  f.write("services/platform/a_test.go", content(800));
  f.write("scripts/example-test.mjs", content(800));
  f.write("apps/platform/empty.ts", "");
  pass(f.run());
  f.write(
    "services/platform/boundary.go",
    content(600).replaceAll("\n", "\r\n"),
  );
  pass(f.run());
  f.write("services/platform/boundary.go", `${content(600)}\n`);
  fail(f.run(), /boundary.go: 601 lines exceeds 600/);
  f.write("services/platform/boundary.go", content(600).trimEnd());
  f.write("apps/platform/a.spec.ts", content(801));
  fail(f.run(), /a.spec.ts: 801 lines exceeds 800/);
});

test("owned generated output and experiments are excluded, adjacent authored source is checked", (t) => {
  const f = fixture(t);
  for (const path of [
    "services/platform/db/generated/queries.go",
    "services/platform/internal/api/api.gen.go",
    "apps/platform/src/api/generated/schema.d.ts",
    "apps/platform/src/i18n/generated/types.ts",
    "apps/platform/.storybook/public/mockServiceWorker.js",
    "apps/platform/styled-system/css/index.mjs",
    "apps/platform/node_modules/library/index.js",
    "apps/platform/dist/assets/index.js",
    "experiments/example/fixture.go",
  ])
    f.write(path, content(1000));
  pass(f.run());
  f.write("services/platform/internal/api/handwritten.go", content(601));
  fail(f.run(), /handwritten.go: 601 lines exceeds 600/);
  rmSync(join(f.root, "services/platform/internal/api/handwritten.go"));
  f.write("apps/platform/src/generated/handwritten.ts", content(601));
  fail(f.run(), /handwritten.ts: 601 lines exceeds 600/);
});

test("output directory names do not hide unrelated authored packages", (t) => {
  const f = fixture(t);
  for (const directory of ["coverage", "dist", "styled-system", ".wrangler"]) {
    const path = `services/platform/internal/${directory}/report.go`;
    f.write(path, content(601));
    fail(f.run(), /report.go: 601 lines exceeds 600/);
    rmSync(join(f.root, path));
  }
});

test("inherited Git selection cannot redirect fixture writes or checker history", (t) => {
  const foreign = fixture(t);
  foreign.write("sentinel.txt", "pending work\n");
  const status = foreign.git("status", "--porcelain=v1");
  const name = foreign.git("config", "user.name");
  const result = spawnSync(
    process.execPath,
    [
      "--test",
      "--test-name-pattern=source and test caps",
      fileURLToPath(import.meta.url),
    ],
    {
      encoding: "utf8",
      env: {
        ...process.env,
        GIT_DIR: join(foreign.root, ".git"),
        GIT_WORK_TREE: foreign.root,
        GIT_INDEX_FILE: join(foreign.root, ".git/index"),
      },
    },
  );
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.equal(foreign.git("rev-parse", "HEAD"), foreign.base);
  assert.equal(foreign.git("config", "user.name"), name);
  assert.equal(foreign.git("status", "--porcelain=v1"), status);
});

test("new oversized files cannot add an allowance through regeneration", (t) => {
  const f = fixture(t);
  f.write("apps/platform/new.ts", content(601));
  fail(f.run(["--write-baseline"]), /new.ts: 601 lines exceeds 600/);
  assert.equal(readFileSync(join(f.root, baseline), "utf8"), "{}\n");
});

test("bootstrap preserves existing debt, then reductions permanently lower its allowance", (t) => {
  const path = "services/platform/legacy.go";
  const f = fixture(t, { [path]: content(701) }, false);
  pass(f.run(["--write-baseline"]));
  pass(f.run());
  const policy = f.commit();
  f.git("switch", "-c", "cleanup");
  f.write(path, content(702));
  fail(f.run(["--base", policy, "--write-baseline"]), /702 lines exceeds 701/);
  assert.equal(
    JSON.parse(readFileSync(join(f.root, baseline), "utf8"))[path],
    701,
  );
  f.write(path, content(690));
  fail(f.run(["--base", policy]), /allowances are stale/);
  pass(f.run(["--base", policy, "--write-baseline"]));
  const reduced = f.commit();
  f.write(path, content(691));
  fail(f.run(["--base", reduced, "--write-baseline"]), /691 lines exceeds 690/);
  f.write(path, content(600));
  pass(f.run(["--base", reduced, "--write-baseline"]));
  assert.equal(readFileSync(join(f.root, baseline), "utf8"), "{}\n");
  pass(f.run(["--base", reduced]));
});

test("first-policy bootstrap cannot grandfather new files or growth", (t) => {
  const f = fixture(t, { "apps/platform/legacy.ts": content(700) }, false);
  f.write("apps/platform/legacy.ts", content(701));
  fail(f.run(["--write-baseline"]), /701 lines exceeds 700/);
  f.write("apps/platform/legacy.ts", content(700));
  f.write("apps/platform/new.ts", content(601));
  fail(f.run(["--write-baseline"]), /new.ts: 601 lines exceeds 600/);
});

test("malformed historical baselines and unavailable history never become bootstrap", (t) => {
  const f = fixture(t);
  f.write(baseline, "[]\n");
  const bad = f.commit();
  fail(f.run(["--base", bad, "--write-baseline"]), /comparison baseline must/);
  fail(f.run(["--base", "missing-ref"]), /Cannot resolve source-size history/);
  fail(f.run([], { CI: "true" }), /CI source-size checks require/);
  const clone = join(f.root, "shallow");
  f.git("clone", "--depth=1", `file://${f.root}`, clone);
  fail(
    f.run(["--base", f.base], { CI: "true" }, clone),
    /Cannot resolve source-size history/,
  );
});

test("the tested merge's first parent catches debt growth hidden by a stale base", (t) => {
  const path = "apps/platform/legacy.ts";
  const f = fixture(t, { [path]: content(701) }, false);
  pass(f.run(["--write-baseline"]));
  const original = f.commit();
  f.git("switch", "-c", "feature");
  f.write(path, content(700));
  pass(f.run(["--base", original, "--write-baseline"]));
  f.commit();
  f.git("switch", "main");
  f.write(path, content(650));
  pass(f.run(["--base", original, "--write-baseline"]));
  f.commit();
  try {
    f.git("merge", "--no-ff", "--no-commit", "feature");
  } catch {}
  f.git("rev-parse", "MERGE_HEAD");
  f.write(path, content(700));
  f.write(baseline, `${JSON.stringify({ [path]: 700 }, null, 2)}\n`);
  f.commit();
  assert.equal(f.git("show", "-s", "--format=%P").split(/\s+/).length, 2);
  pass(f.run(["--base", original]));
  fail(
    f.run([], { CI: "true", SOURCE_SIZE_BASE: "HEAD^1" }),
    /700 lines exceeds 650/,
  );
});

test("archives report missing history and cannot regenerate or bypass CI", (t) => {
  const root = mkdtempSync(join(tmpdir(), "astilba-source-archive-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  mkdirSync(join(root, "scripts"));
  writeFileSync(join(root, baseline), "{}\n");
  const run = (args = [], env = {}) =>
    spawnSync(process.execPath, [script, ...args], {
      cwd: root,
      encoding: "utf8",
      env: { ...process.env, CI: "", SOURCE_SIZE_BASE: "", ...env },
    });
  const result = run();
  pass(result);
  assert.match(result.stdout, /historical ratchet unavailable/);
  fail(run(["--write-baseline"]), /requires Git comparison history/);
  fail(run([], { CI: "true" }), /requires a Git checkout/);
  fail(run(["--base", "HEAD"]), /requires a Git checkout/);
});

test("source and baseline symlinks cannot read or overwrite external targets", (t) => {
  const f = fixture(t);
  const external = join(f.root, "external.txt");
  f.write("external.txt", "retained\n");
  mkdirSync(join(f.root, "apps/platform"), { recursive: true });
  const linked = join(f.root, "apps/platform/linked.ts");
  symlinkSync(external, linked);
  fail(f.run(), /refuse symlinks/);
  rmSync(linked);
  rmSync(join(f.root, baseline));
  symlinkSync(external, join(f.root, baseline));
  fail(f.run(["--write-baseline"]), /refuse symlinks|regular file/);
  assert.equal(readFileSync(external, "utf8"), "retained\n");
});
