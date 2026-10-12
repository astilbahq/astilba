import { spawnSync } from "node:child_process";
import path from "node:path";

const cwd = path.resolve(import.meta.dirname, "../../packages/env");
const lanes = [
  ["scripts/verify-consumer.mjs", "npm"],
  ["scripts/verify-consumer.mjs", "pnpm"],
  ["scripts/verify-consumer.mjs", "bun"],
  ["scripts/verify-workers-consumer.mjs"],
  ["scripts/verify-portable-equivalence.mjs"],
  ["scripts/verify-deterministic-generation.mjs"],
  ["scripts/verify-vite-consumer.mjs"],
];
for (const next of ["15.5.22", "16.2.12"]) {
  for (const mode of [
    "app-static",
    "app-request",
    "pages-static",
    "pages-request",
  ]) {
    lanes.push(["scripts/verify-next-consumer.mjs", next, mode]);
  }
}
for (const lane of lanes) {
  const result = spawnSync(process.execPath, lane, { cwd, stdio: "inherit" });
  if (result.error || result.status !== 0 || result.signal) {
    throw new Error(`Env consumer lane failed: ${lane.join(" ")}`, {
      cause: result.error,
    });
  }
}
