import { checksFor, runChecks } from "./checks.mjs";
import { emit, openSession, readInput, sessionWorktree } from "./session.mjs";

const MAX_BLOCKS_PER_PROMPT = 3;
const BUDGET_MS = 90_000;
// These can still edit the worktree, and their completion ends another turn.
const editing = new Set(["subagent", "workflow", "teammate"]);

const names = (results) => results.map(({ name }) => name).join(", ");
const failureReport = (failed, skipped) =>
  [
    "Fast checks failed on files changed in this session. Fix them before finishing. If one cannot be fixed here, leave the files unchanged and say so in your final message: this hook does not block again on unchanged files.",
    ...failed.map(
      ({ name, cmd, detail }) =>
        `[${name}] ${cmd.slice(0, 4).join(" ")}\n${detail}`,
    ),
    ...(skipped.length ? [`Not run: ${names(skipped)}`] : []),
  ].join("\n\n");

try {
  const input = readInput();
  const repo = sessionWorktree(input.cwd);
  const session = openSession(repo, input.session_id);
  session.ensureBaseline();
  const state = session.state();
  const files = session.changedFiles(state);
  const fingerprint = session.fingerprint(files);
  const pending = input.background_tasks?.some(({ type }) => editing.has(type));
  if (files.length && fingerprint !== state.settled && !pending) {
    // A continuation forced by this hook never earns a fresh set of attempts.
    const samePrompt =
      input.stop_hook_active || input.prompt_id === state.prompt;
    const blocks = samePrompt ? (state.blocks ?? 0) : 0;
    const settle = (message) => {
      session.save({
        ...state,
        settled: fingerprint,
        failed: undefined,
        blocks: 0,
      });
      if (message) emit({ systemMessage: message });
    };
    if (samePrompt && fingerprint === state.failed) {
      settle(
        "Project checks still fail on unchanged files; not blocking again.",
      );
    } else {
      const results = await runChecks(
        repo.root,
        checksFor(repo.root, files),
        BUDGET_MS,
      );
      const failed = results.filter(({ status }) => status === "failed");
      const skipped = results.filter(({ status }) => status === "skipped");
      if (!failed.length) {
        settle(
          skipped.length
            ? `Project checks not run: ${skipped.map(({ name, detail }) => `${name} (${detail})`).join("; ")}`
            : undefined,
        );
      } else if (blocks >= MAX_BLOCKS_PER_PROMPT) {
        settle(
          `Project checks still fail after ${blocks} attempts: ${names(failed)}`,
        );
      } else {
        session.save({
          ...state,
          failed: fingerprint,
          blocks: blocks + 1,
          prompt: input.prompt_id,
        });
        emit({ decision: "block", reason: failureReport(failed, skipped) });
      }
    }
  }
} catch (error) {
  emit({ systemMessage: `Project stop hook failed: ${error.message}` });
}
