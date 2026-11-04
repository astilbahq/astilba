import { emit, openSession, readInput, sessionWorktree } from "./session.mjs";

try {
  const input = readInput();
  const session = openSession(sessionWorktree(input.cwd), input.session_id);
  session.ensureBaseline();
  session.prune();
} catch (error) {
  emit({ systemMessage: `Project session hook failed: ${error.message}` });
}
