import { execFileSync } from "node:child_process";
import { realpathSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { formattable } from "./checks.mjs";
import { emit, openSession, readInput, worktreeOf } from "./session.mjs";

const FORMAT_TIMEOUT_MS = 20_000;

function format(root, path) {
  const options = { cwd: root, stdio: "ignore", timeout: FORMAT_TIMEOUT_MS };
  try {
    if (path.endsWith(".go"))
      execFileSync("gofmt", ["-w", `./${path}`], options);
    else if (formattable(path))
      execFileSync(
        join(root, "node_modules/.bin/vp"),
        ["fmt", "--write", "--no-error-on-unmatched-pattern", `./${path}`],
        options,
      );
  } catch {
    // A missing formatter or invalid syntax must not fail the edit; the Stop
    // hook reports what is still unformatted.
  }
}

try {
  const input = readInput();
  const file = realpathSync(input.tool_input.file_path);
  const repo = worktreeOf(dirname(file));
  if (repo) {
    const path = relative(repo.root, file);
    const session = openSession(repo, input.session_id);
    session.ensureBaseline();
    session.recordEdit(path);
    format(repo.root, path);
  }
} catch (error) {
  emit({ systemMessage: `Project edit hook failed: ${error.message}` });
}
