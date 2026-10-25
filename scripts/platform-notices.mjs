import { execFileSync } from "node:child_process";
import {
  mkdir,
  readdir,
  readFile,
  realpath,
  stat,
  writeFile,
} from "node:fs/promises";
import { dirname, join, resolve } from "node:path";

const root = resolve(import.meta.dirname, "..");
const target = resolve(
  process.argv[2] ?? join(root, "apps/platform/dist/source"),
);
const revision = process.env.SOURCE_REVISION ?? "development";
if (revision !== "development" && !/^[a-f0-9]{40}$/.test(revision))
  throw new Error("SOURCE_REVISION must be a full commit hash");
const modules = new Map();
const output = execFileSync(
  "go",
  [
    "list",
    "-deps",
    "-f",
    "{{if .Module}}{{.Module.Path}}|{{.Module.Version}}|{{.Module.Dir}}{{end}}",
    "./cmd/server",
    "./cmd/agent",
    "./cmd/cli",
  ],
  { cwd: join(root, "services/platform"), encoding: "utf8" },
);
for (const line of output.split("\n")) {
  const [name, version, dir] = line.split("|");
  if (version && dir) modules.set(`${name}@${version}`, dir);
}
async function locate(name, from) {
  let dir = from;
  for (;;) {
    const candidate = join(dir, "node_modules", name);
    try {
      await stat(join(candidate, "package.json"));
      return realpath(candidate);
    } catch {}
    const parent = dirname(dir);
    if (parent === dir)
      throw new Error(`Cannot resolve runtime package ${name}`);
    dir = parent;
  }
}
async function visit(name, from) {
  const dir = await locate(name, from);
  const pkg = JSON.parse(await readFile(join(dir, "package.json"), "utf8"));
  const key = `${pkg.name}@${pkg.version}`;
  if (modules.has(key)) return;
  modules.set(key, dir);
  for (const child of Object.keys(pkg.dependencies ?? {}))
    await visit(child, dir);
}
const app = join(root, "apps/platform");
const pkg = JSON.parse(await readFile(join(app, "package.json"), "utf8"));
for (const name of Object.keys(pkg.dependencies)) await visit(name, app);
let notice = `Astilba platform\nCopyright Astilba contributors. Licensed under GNU AGPL version 3.\nRevision: ${revision}\nCorresponding source: ${revision === "development" ? "This development checkout" : `https://github.com/astilbahq/astilba/tree/${revision}`}\nReproduce with the repository Dockerfile, pinned lockfile and scripts.\n\nThird-party runtime notices follow.\n\n`;
for (const [name, dir] of [...modules].sort(([a], [b]) => a.localeCompare(b))) {
  const files = (await readdir(dir)).filter((n) =>
    /^(licen[cs]e|copying|notice)(\.|$)/i.test(n),
  );
  if (files.length === 0) throw new Error(`Missing runtime licence: ${name}`);
  notice += `===== ${name} =====\n`;
  for (const file of files.sort()) {
    if ((await stat(join(dir, file))).isFile())
      notice += `${file}\n${await readFile(join(dir, file), "utf8")}\n`;
  }
}
// qrcode.react's npm package omits the licence for its bundled encoder.
notice += `===== QR Code Generator (bundled by qrcode.react) =====\n${await readFile(join(app, "LICENSE.qrcodegen"), "utf8")}\n`;
const goroot = execFileSync("go", ["env", "GOROOT"], {
  encoding: "utf8",
}).trim();
let goLicense;
try {
  goLicense = await readFile(join(goroot, "LICENSE"), "utf8");
} catch (error) {
  if (error.code !== "ENOENT") throw error;
  goLicense = await readFile(join(goroot, "..", "LICENSE"), "utf8");
}
notice += `===== Go standard library =====\n${goLicense}`;
await mkdir(target, { recursive: true });
await writeFile(join(target, "NOTICE.txt"), notice);
await writeFile(
  join(target, "LICENSE.txt"),
  await readFile(join(root, "LICENSE")),
);
console.log("Runtime notices written.");
