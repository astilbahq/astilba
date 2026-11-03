import { execFileSync, spawnSync } from "node:child_process";
import { lstatSync } from "node:fs";
import { join } from "node:path";

export const gitEnvironment = {
  ...Object.fromEntries(
    Object.entries(process.env).filter(([name]) => !name.startsWith("GIT_")),
  ),
  GIT_NO_LAZY_FETCH: "1",
};

// Resolves the commit a ratchet compares against: SOURCE_SIZE_BASE or --base in
// CI, otherwise the merge base with main. Returns null for a Git-free source
// archive outside CI.
export function comparisonBase(root, requested, ci, name) {
  const git = (...args) =>
    execFileSync("git", ["--literal-pathspecs", ...args], {
      cwd: root,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
      maxBuffer: 10 * 1024 * 1024,
      env: gitEnvironment,
    });
  let hasGit;
  try {
    lstatSync(join(root, ".git"));
    hasGit = true;
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
    hasGit = false;
  }
  if (!hasGit) {
    if (ci || requested)
      throw new Error(
        `${name} history requires a Git checkout and a valid comparison base`,
      );
    return null;
  }
  if (ci && !requested)
    throw new Error(`CI ${name} checks require SOURCE_SIZE_BASE or --base`);
  let base;
  try {
    if (requested) {
      base = git(
        "rev-parse",
        "--verify",
        "--end-of-options",
        `${requested}^{commit}`,
      ).trim();
    } else {
      let ref;
      for (const candidate of ["refs/remotes/origin/main", "refs/heads/main"]) {
        const result = spawnSync(
          "git",
          ["show-ref", "--verify", "--quiet", candidate],
          { cwd: root, env: gitEnvironment },
        );
        if (result.error) throw result.error;
        if (result.status === 0) {
          ref = candidate;
          break;
        }
        if (result.status !== 1)
          throw new Error("Cannot inspect comparison refs");
      }
      if (!ref) throw new Error("No main comparison ref");
      base = git("merge-base", "HEAD", ref).trim();
    }
    git("cat-file", "-e", `${base}^{commit}`);
  } catch {
    throw new Error(
      `Cannot resolve ${name} history; fetch main/history or set SOURCE_SIZE_BASE to an available commit`,
    );
  }
  const entry = (path) => {
    const result = git("ls-tree", "-z", base, "--", path);
    if (!result) return null;
    const mode = result.slice(0, result.indexOf(" "));
    if (!["100644", "100755"].includes(mode))
      throw new Error(`Comparison source must be a regular file: ${path}`);
    return git("show", `${base}:${path}`);
  };
  // Lists regular files directly inside a directory at the comparison base.
  const files = (directory) =>
    git("ls-tree", "-z", "--name-only", base, "--", `${directory}/`)
      .split("\0")
      .filter(Boolean);
  return { base, entry, files };
}
