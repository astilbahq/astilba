// Replication-reader conformance — the REAL Invalidation passes the reader contract.
// Non-sacred (CLAUDE.md rule 11); the same block will pin the deployed reader path in a
// later cluster. Deterministic lane: attempts/ticks are injected explicitly.

import { makeReferenceReaderHarness } from "./fixtures/reference";
import { runReaderContract } from "./runner/reader";

runReaderContract(makeReferenceReaderHarness(), {
  label: "Invalidation (reader reference)",
});
