import { spawnSync } from "node:child_process";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "../..");
const cwd = path.resolve(root, "fixtures/registry/env");
const commands = [
  ["oxfmt", "--config", "oxfmt.config.mts", "--check", "."],
  ["oxlint", "--config", "oxlint.config.mts", "."],
];
for (const [tool, ...arguments_] of commands) {
  const cli = path.resolve(
    root,
    "packages/env/node_modules",
    tool,
    "bin",
    tool
  );
  const result = spawnSync(process.execPath, [cli, ...arguments_], {
    cwd,
    stdio: "inherit",
  });
  if (result.error || result.status !== 0 || result.signal) {
    throw new Error(`Registry fixture ${tool} check failed.`, {
      cause: result.error,
    });
  }
}
