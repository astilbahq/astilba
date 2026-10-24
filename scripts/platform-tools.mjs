import { execFileSync, spawn } from "node:child_process";
export function pnpm(args, options = {}) {
  if (process.versions.node !== "24.18.1" || !process.env.npm_execpath)
    throw new Error("Run this command through pnpm 11.17.0 with Node 24.18.1");
  return execFileSync(
    process.execPath,
    [process.env.npm_execpath, ...args],
    options,
  );
}
export function spawnPnpm(args, options = {}) {
  if (!process.env.npm_execpath)
    throw new Error("Run this command through pnpm");
  return spawn(process.execPath, [process.env.npm_execpath, ...args], options);
}
