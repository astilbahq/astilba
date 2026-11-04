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
const sharedState = () => join(own.common, "claude-hooks");
const sessionName = (sessionId) => encodeURIComponent(sessionId);
const worktreeLog = (sessionId) =>
  join(sharedState(), `${sessionName(sessionId)}.worktrees`);

function listing(directory) {
  try {
    return readdirSync(directory);
  } catch (error) {
    if (error.code === "ENOENT") return [];
    throw error;
  }
}

function entries(path) {
  try {
    return readFileSync(path, "utf8").split("\0").filter(Boolean);
  } catch (error) {
    if (error.code === "ENOENT") return [];
    throw error;
  }
}

// The worktree holding a path, or null when it belongs to another repository:
// a session can `cd` elsewhere, but these hooks only speak for this one.
export function worktreeOf(directory) {
  const repo = repoAt(directory);
  return repo && repo.common === own.common ? repo : null;
}

// The worktree a session is working in. State lives in that worktree's Git
// directory, so it stays out of `git status` and is removed with the worktree.
export const sessionWorktree = (cwd) => worktreeOf(cwd) ?? own;

// Every worktree this session has touched that still exists, current one first.
export function sessionWorktrees(cwd, sessionId) {
  const repos = new Map();
  for (const repo of [
    sessionWorktree(cwd),
    ...entries(worktreeLog(sessionId)).map(worktreeOf),
  ])
    if (repo && !repos.has(repo.root)) repos.set(repo.root, repo);
  return [...repos.values()];
}

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

// Files in commits made since the baseline. Committer dates leave out commits
// that a pull, reset or branch switch brought in, and a rebase's new dates keep
// the session's own. An unborn HEAD has no commits to report.
function committed(root, since) {
  try {
    return git(
      root,
      "log",
      "--no-merges",
      "--no-renames",
      "--name-only",
      "-z",
      "--format=",
      `--since=${since}`,
    )
      .split("\0")
      .filter(Boolean);
  } catch {
    return [];
  }
}

export function openSession(repo, sessionId) {
  const id = sessionName(sessionId);
  const statePath = join(repo.state, `${id}.json`);
  const editsPath = join(repo.state, `${id}.edits`);
  mkdirSync(repo.state, { recursive: true });
  const edits = () => entries(editsPath);

  return {
    // The first hook to see a worktree records what was already changed, so a
    // session answers only for its own work. Anything but session start may be
    // late, with earlier work already in that record. Returns whether it wrote.
    ensureBaseline(late = true) {
      const state = {
        base: {
          since: Math.floor(Date.now() / 1000),
          dirty: Object.fromEntries(
            dirty(repo.root).map((path) => [
              path,
              stamp(join(repo.root, path)),
            ]),
          ),
        },
        ...(late ? { late } : {}),
      };
      try {
        writeFileSync(statePath, JSON.stringify(state), { flag: "wx" });
      } catch (error) {
        if (error.code === "EEXIST") return false;
        throw error;
      }
      mkdirSync(sharedState(), { recursive: true });
      appendFileSync(worktreeLog(sessionId), `${repo.root}\0`);
      return true;
    },

    state: () => JSON.parse(readFileSync(statePath, "utf8")),
    save: (state) => writeFileSync(statePath, JSON.stringify(state)),
    edits,
    recordEdit: (path) => appendFileSync(editsPath, `${path}\0`),

    // Files edited through tools, plus anything Git shows changed since the
    // baseline, which is how Bash writes and commits are detected.
    changedFiles({ base }) {
      const changed = new Set([
        ...edits(),
        ...committed(repo.root, base.since),
      ]);
      for (const path of dirty(repo.root))
        if (base.dirty[path] !== stamp(join(repo.root, path)))
          changed.add(path);
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
      for (const directory of new Set([repo.state, sharedState()]))
        for (const name of listing(directory)) {
          const path = join(directory, name);
          const age = statSync(path, { throwIfNoEntry: false })?.mtimeMs;
          if (age && Date.now() - age > PRUNE_AFTER_MS)
            rmSync(path, { force: true });
        }
    },
  };
}
