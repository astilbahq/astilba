import { checksFor, runChecks } from "./checks.mjs";
import { emit, openSession, readInput, sessionWorktrees } from "./session.mjs";

const MAX_BLOCKS_PER_PROMPT = 3;
const BUDGET_MS = 90_000;
// These can still edit the worktree, and their completion ends another turn.
const editing = new Set(["subagent", "workflow"]);

const names = (results) => results.map(({ name }) => name).join(", ");
const failureReport = (root, failed, skipped) =>
  [
    `Fast checks failed in ${root}:`,
    ...failed.map(
      ({ name, cmd, detail }) =>
        `[${name}] ${cmd.slice(0, 4).join(" ")}\n${detail}`,
    ),
    ...(skipped.length ? [`Not run: ${names(skipped)}`] : []),
  ].join("\n\n");

async function checkWorktree(repo, input, deadline, current) {
  const session = openSession(repo, input.session_id);
  session.ensureBaseline();
  const state = session.state();
  const notes = [];
  // A new prompt earns fresh attempts. A continuation forced by this hook never
  // does, and settling keeps the count: another worktree can keep the turn going.
  if (!input.stop_hook_active && input.prompt_id !== state.prompt) {
    state.prompt = input.prompt_id;
    state.blocks = 0;
    delete state.failed;
    session.save(state);
  }
  if (current && state.late) {
    notes.push(
      `Project checks in ${repo.root} only cover changes made after these hooks first saw it.`,
    );
    delete state.late;
    session.save(state);
  }
  const files = session.changedFiles(state, current);
  const fingerprint = session.fingerprint(files);
  if (!files.length || fingerprint === state.settled) return { notes };
  if (input.background_tasks?.some(({ type }) => editing.has(type))) {
    notes.push("Project checks deferred until background agents finish.");
    return { notes };
  }

  const blocks = state.blocks ?? 0;
  const settle = (...messages) => {
    session.save({ ...state, settled: fingerprint, failed: undefined, blocks });
    return { notes: [...notes, ...messages] };
  };
  if (fingerprint === state.failed)
    return settle(
      `Project checks failed in ${repo.root} and those files have not changed since, so they were not rerun.`,
    );

  const results = await runChecks(
    repo.root,
    checksFor(repo.root, files),
    deadline,
  );
  const failed = results.filter(({ status }) => status === "failed");
  const skipped = results.filter(({ status }) => status === "skipped");
  if (!failed.length)
    return skipped.length
      ? settle(
          `Project checks not run: ${skipped.map(({ name, detail }) => `${name} (${detail})`).join("; ")}`,
        )
      : settle();
  if (blocks >= MAX_BLOCKS_PER_PROMPT)
    return settle(
      `Project checks still fail after ${blocks} attempts: ${names(failed)}`,
    );
  session.save({
    ...state,
    failed: fingerprint,
    blocks: blocks + 1,
  });
  return { notes, block: failureReport(repo.root, failed, skipped) };
}

try {
  const input = readInput();
  const deadline = Date.now() + BUDGET_MS;
  const outcomes = await Promise.all(
    sessionWorktrees(input.cwd, input.session_id).map((repo, index) =>
      checkWorktree(repo, input, deadline, index === 0).catch((error) => ({
        notes: [`Project stop hook failed in ${repo.root}: ${error.message}`],
      })),
    ),
  );
  const blocks = outcomes.flatMap(({ block }) => block ?? []);
  const notes = outcomes.flatMap((outcome) => outcome.notes);
  if (blocks.length || notes.length)
    emit({
      ...(blocks.length && {
        decision: "block",
        reason: [
          "Fix these failures on files changed in this session before finishing. If one cannot be fixed here, leave the files unchanged and say so in your final message: this hook does not block again on unchanged files.",
          ...blocks,
        ].join("\n\n"),
      }),
      ...(notes.length && { systemMessage: notes.join(" ") }),
    });
} catch (error) {
  emit({ systemMessage: `Project stop hook failed: ${error.message}` });
}
