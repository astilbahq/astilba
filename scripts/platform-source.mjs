import { execFileSync } from "node:child_process";
import { mkdir } from "node:fs/promises";
import { join, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const revision = process.env.SOURCE_REVISION;
if (!revision || !/^[a-f0-9]{40}$/.test(revision))
  throw new Error("A full SOURCE_REVISION is required for the source artifact");
const out = join(root, ".artifacts/p1");
await mkdir(out, { recursive: true });
execFileSync(
  "git",
  [
    "archive",
    "--format=tar.gz",
    `--prefix=astilba-${revision}/`,
    `--output=${out}/astilba-${revision}.tar.gz`,
    revision,
  ],
  { cwd: root, stdio: "inherit" },
);
