import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import { downloadVerified } from "./fetch-attempts.mjs";

// The version comes from package.json's packageManager and the hash from pnpm's own
// lockfile entry, which Dependabot moves with the catalog. See
// docs/frontend-development.md#dependency-pins.
export function pnpmPin({ manifest, lockfile }) {
  const version = /^pnpm@(\d+\.\d+\.\d+)$/.exec(manifest.packageManager)?.[1];
  if (!version)
    throw new Error(
      `package.json: packageManager must be pnpm@<exact version>, found ${manifest.packageManager}`,
    );
  const entry = new RegExp(
    `^ {2}pnpm@${version.replaceAll(".", "\\.")}:\\n {4}resolution: \\{integrity: sha512-([A-Za-z0-9+/]+={0,2})\\}$`,
    "m",
  ).exec(lockfile);
  if (!entry)
    throw new Error(`pnpm-lock.yaml: no sha512 integrity for pnpm@${version}`);
  const hash = Buffer.from(entry[1], "base64").toString("hex");
  if (hash.length !== 128)
    throw new Error(`pnpm-lock.yaml: pnpm@${version} has a malformed sha512`);
  return { version, hash };
}

// pnpm has no dependencies, so an offline install contacts nothing else, and it
// installs the bytes just checked rather than asking the registry again.
export async function installPnpm({
  manifest,
  lockfile,
  registry = "https://registry.npmjs.org",
  run = execFileSync,
  ...options
}) {
  const { version, hash } = pnpmPin({ manifest, lockfile });
  const bytes = await downloadVerified(
    `${registry}/pnpm/-/pnpm-${version}.tgz`,
    "sha512",
    hash,
    options,
  );
  const directory = mkdtempSync(join(tmpdir(), "astilba-pnpm-"));
  try {
    const tarball = join(directory, `pnpm-${version}.tgz`);
    writeFileSync(tarball, bytes);
    run(
      "npm",
      [
        "install",
        "--global",
        "--offline",
        "--ignore-scripts",
        "--no-audit",
        "--no-fund",
        tarball,
      ],
      { stdio: "inherit" },
    );
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  const root = resolve(import.meta.dirname, "..");
  await installPnpm({
    manifest: JSON.parse(readFileSync(join(root, "package.json"), "utf8")),
    lockfile: readFileSync(join(root, "pnpm-lock.yaml"), "utf8"),
  });
}
