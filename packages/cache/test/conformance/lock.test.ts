// Lock conformance — the v0.1 FakeLock passes the Lock contract. Non-sacred
// (CLAUDE.md rule 11); a real DO/Redis-primary lock passes the identical block later.

import { makeFakeLockHarness } from "./fixtures/reference";
import { runLockContract } from "./runner/lock";

runLockContract(makeFakeLockHarness(), {
  label: "FakeLock (v0.1 reference)",
});
