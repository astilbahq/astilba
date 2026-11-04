import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  appendFileSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { join } from "node:path";

const PRUNE_AFTER_MS = 14 * 24 * 60 * 60 * 1000;
const env = {
  ...Object.fromEntries(
    Object.entries(process.env).filter(([name]) => !name.startsWith("GIT_")),
  ),
  GIT_NO_LAZY_FETCH: "1",
  GIT_OPTIONAL_LOCKS: "0",
};
const git = (cwd, ...args) =>
  execFileSync("git", args, {
    cwd,
    env,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "ignore"],
    maxBuffer: 64 * 1024 * 1024,
  });

export const readInput = () => JSON.parse(readFileSync(0, "utf8"));
export const emit = (output) =>
  process.stdout.write(`${JSON.stringify(output)}\n`);

function repoAt(directory) {
  try {
    const [root, common, state] = git(
      directory,
      "rev-parse",
      "--path-format=absolute",
      "--show-toplevel",
      "--git-common-dir",
      "--git-path",
      "claude-hooks",
    )
      .trim()
      .split("\n");
    return { root, common, state };
  } catch {
    return null;
  }
}

const own = repoAt(import.meta.dirname);

// The worktree holding a path, or null when it belongs to another repository:
// a session can `cd` elsewhere, but these hooks only speak for this one.
export function worktreeOf(directory) {
  const repo = repoAt(directory);
  return repo && repo.common === own.common ? repo : null;
}

// The worktree a session is working in. State lives in that worktree's Git
// directory, so it stays out of `git status` and is removed with the worktree.
export const sessionWorktree = (cwd) => worktreeOf(cwd) ?? own;

function stamp(path) {
  try {
    const { size, mtimeMs } = statSync(path);
    return `${size}:${mtimeMs}`;
  } catch {
    return "missing";
  }
}

function dirty(root) {
  return git(root, "status", "--porcelain=v1", "-z", "-uall", "--no-renames")
    .split("\0")
    .filter(Boolean)
    .map((entry) => entry.slice(3));
}

function head(root) {
  try {
    return git(root, "rev-parse", "--verify", "HEAD").trim();
  } catch {
    return "";
  }
}

export function openSession(repo, sessionId) {
  const id = encodeURIComponent(sessionId ?? "unknown");
  const statePath = join(repo.state, `${id}.json`);
  const editsPath = join(repo.state, `${id}.edits`);
  mkdirSync(repo.state, { recursive: true });

  // The first hook to see a worktree records what was already changed, so a
  // session answers only for its own work. The session's state is never reset.
  function ensureBaseline() {
    const dirtyFiles = Object.fromEntries(
      dirty(repo.root).map((path) => [path, stamp(join(repo.root, path))]),
    );
    const state = { base: { head: head(repo.root), dirty: dirtyFiles } };
    try {
      writeFileSync(statePath, JSON.stringify(state), { flag: "wx" });
    } catch (error) {
      if (error.code !== "EEXIST") throw error;
    }
  }

  return {
    ensureBaseline,
    state: () => JSON.parse(readFileSync(statePath, "utf8")),
    save: (state) => writeFileSync(statePath, JSON.stringify(state)),
    recordEdit: (path) => appendFileSync(editsPath, `${path}\n`),

    // Files edited through tools, plus anything Git shows changed since the
    // baseline, which is how Bash writes and commits are detected.
    changedFiles({ base }) {
      const changed = new Set();
      try {
        readFileSync(editsPath, "utf8")
          .split("\n")
          .filter(Boolean)
          .forEach((path) => changed.add(path));
      } catch (error) {
        if (error.code !== "ENOENT") throw error;
      }
      for (const path of dirty(repo.root))
        if (base.dirty[path] !== stamp(join(repo.root, path)))
          changed.add(path);
      const current = head(repo.root);
      if (base.head && current !== base.head)
        git(
          repo.root,
          "diff",
          "--name-only",
          "-z",
          "--no-renames",
          base.head,
          current,
        )
          .split("\0")
          .filter(Boolean)
          .forEach((path) => changed.add(path));
      return [...changed].sort();
    },

    fingerprint: (files) =>
      createHash("sha256")
        .update(
          files
            .map((path) => `${path}\0${stamp(join(repo.root, path))}`)
            .join("\n"),
        )
        .digest("hex"),

    prune() {
      for (const name of readdirSync(repo.state)) {
        const path = join(repo.state, name);
        if (Date.now() - statSync(path).mtimeMs > PRUNE_AFTER_MS) rmSync(path);
      }
    },
  };
}
