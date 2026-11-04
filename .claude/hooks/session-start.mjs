import { emit, openSession, readInput, sessionWorktree } from "./session.mjs";

try {
  const input = readInput();
  const session = openSession(sessionWorktree(input.cwd), input.session_id);
  // Pruning first keeps it from deleting the baseline this session is about to use.
  session.prune();
  session.ensureBaseline(false);
} catch (error) {
  emit({ systemMessage: `Project session hook failed: ${error.message}` });
}
