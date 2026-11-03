import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { devNull, tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { inlineSQL } from "./inline-sql.mjs";

const script = fileURLToPath(new URL("./inline-sql.mjs", import.meta.url));
const environment = {
  ...Object.fromEntries(
    Object.entries(process.env).filter(
      ([name]) => !name.startsWith("GIT_") && name !== "SOURCE_SIZE_BASE",
    ),
  ),
  CI: "",
  GIT_CONFIG_NOSYSTEM: "1",
  GIT_CONFIG_GLOBAL: devNull,
};

test("counts statement literals and ignores comments, runes, prose and exceptions", () => {
  for (const [source, expected] of [
    ["db.Exec(ctx, `SELECT 1`)", 1],
    ['q := "SELECT " + columns + " FROM t WHERE id=$1"', 1],
    ['s := []string{"DELETE FROM a", "UPDATE b SET c=1"}', 2],
    ['m := "say \\"SELECT 1\\""', 0],
    ["// SELECT x\n/* UPDATE y */", 0],
    ['r := \'"\'\nx := "Update failed"', 0],
    [
      "// inline-sql: library-owned River table\nq := `DELETE FROM river_job`",
      0,
    ],
    ["q := `ALTER TABLE t` // inline-sql: customer-managed database", 0],
    ["// inline-sql:\nq := `SELECT 1`", 1],
    ["a := `SELECT 1` // inline-sql: lock\nb := `DELETE FROM t`", 1],
  ])
    assert.equal(inlineSQL(source), expected, source);
});

function repository(t) {
  const cwd = mkdtempSync(join(tmpdir(), "astilba-inline-sql-"));
  t.after(() => rmSync(cwd, { recursive: true, force: true }));
  const git = (...args) =>
    execFileSync("git", args, { cwd, encoding: "utf8", env: environment });
  const write = (path, content) => {
    mkdirSync(dirname(join(cwd, path)), { recursive: true });
    writeFileSync(join(cwd, path), content);
  };
  git("init", "--quiet", "--initial-branch=main");
  git("config", "user.name", "Inline SQL fixture");
  git("config", "user.email", "ci@example.invalid");
  write("services/platform/a.go", "q := `SELECT 1`\nr := `UPDATE t SET x=1`\n");
  write("services/platform/db/generated/q.go", "q := `SELECT 2`\n");
  git("add", "-A");
  git("commit", "--quiet", "-m", "base");
  const base = git("rev-parse", "HEAD").trim();
  const check = (extra = {}) =>
    spawnSync(process.execPath, [script, "--base", base], {
      cwd,
      encoding: "utf8",
      env: { ...environment, ...extra },
    });
  return { cwd, write, check };
}

test("inline SQL may move within a package but not grow or enter a new package", async (t) => {
  const a = "services/platform/a.go";
  const b = "services/platform/b.go";
  for (const [label, writes, ok] of [
    ["shrink", [[a, "q := `SELECT 1`\n"]], true],
    [
      "split within the package",
      [
        [a, "q := `SELECT 1`\n"],
        [b, "r := `UPDATE t SET x=1`\n"],
      ],
      true,
    ],
    [
      "grow in place",
      [[a, "q := `SELECT 1`\nr := `UPDATE t SET x=1`\ns := `DELETE FROM t`\n"]],
      false,
    ],
    [
      "grow through a new file",
      [[b, "q := `INSERT INTO t VALUES(1)`\n"]],
      false,
    ],
    [
      "move to another package",
      [
        [a, "q := `SELECT 1`\n"],
        ["services/platform/internal/x/c.go", "r := `UPDATE t SET x=1`\n"],
      ],
      false,
    ],
    [
      "annotated exception",
      [[b, "// inline-sql: customer-managed database\nq := `SELECT 1`\n"]],
      true,
    ],
    [
      "tests are not counted",
      [["services/platform/b_test.go", "q := `SELECT 1`\n"]],
      true,
    ],
    [
      "generated output is not counted",
      [
        [
          "services/platform/db/generated/q.go",
          "q := `SELECT 2`\nr := `SELECT 3`\n",
        ],
      ],
      true,
    ],
  ])
    await t.test(label, (t) => {
      const repo = repository(t);
      for (const [path, content] of writes) repo.write(path, content);
      const result = repo.check();
      assert.equal(result.status === 0, ok, result.stdout + result.stderr);
      if (!ok) assert.match(result.stderr, /sqlc|inline-sql: <reason>/);
    });
});

test("CI refuses to run without an explicit comparison base", (t) => {
  const repo = repository(t);
  const result = spawnSync(process.execPath, [script], {
    cwd: repo.cwd,
    encoding: "utf8",
    env: { ...environment, CI: "true" },
  });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /require SOURCE_SIZE_BASE or --base/);
});
