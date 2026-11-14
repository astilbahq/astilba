import { execFileSync } from "node:child_process";
export function pnpm(args, options = {}) {
  if (process.versions.node !== "24.22.3" || !process.env.npm_execpath)
    throw new Error("Run this command through pnpm with Node 24.22.3");
  return execFileSync(
    process.execPath,
    [process.env.npm_execpath, ...args],
    options,
  );
}
