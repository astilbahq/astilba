import { execFileSync } from "node:child_process";
import { cp, mkdir, rm } from "node:fs/promises";
import { join, resolve } from "node:path";
import { pnpm } from "./platform-tools.mjs";

const root = resolve(import.meta.dirname, "..");
const out = resolve(process.argv[2] ?? join(root, ".artifacts/p1/astilba"));
const revision = process.env.SOURCE_REVISION ?? "development";
if (revision !== "development" && !/^[a-f0-9]{40}$/.test(revision))
  throw new Error("SOURCE_REVISION must be a full commit hash");
pnpm(["--filter", "@astilba/dashboard", "build"], {
  cwd: root,
  stdio: "inherit",
});
execFileSync(process.execPath, [join(root, "scripts/platform-notices.mjs")], {
  stdio: "inherit",
  env: { ...process.env, SOURCE_REVISION: revision },
});
const embed = join(root, "services/platform/internal/webassets/dist");
await rm(embed, { recursive: true, force: true });
await cp(join(root, "apps/platform/dist"), embed, { recursive: true });
await mkdir(resolve(out, ".."), { recursive: true });
execFileSync(
  "go",
  [
    "build",
    "-trimpath",
    "-tags",
    "embedui",
    "-ldflags",
    `-s -w -X main.revision=${revision}`,
    "-o",
    out,
    "./cmd/server",
  ],
  { cwd: join(root, "services/platform"), stdio: "inherit" },
);
