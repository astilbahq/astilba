import { cloudflareTest } from "@cloudflare/vitest-pool-workers";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

// The integration lane: the fourth test project, run on real workerd
// via @cloudflare/vitest-pool-workers. Kept as its own project config file (referenced
// by path from the root vite.config.ts `test.projects`) rather than an inline entry:
// vitest 4 removed `test.poolOptions`, so the pool is wired through the `cloudflareTest`
// Vite plugin, and authoring it here keeps its plugin/vite types clear of the root
// config's vp-bundled-vite type surface. Timing tolerance is legal in THIS lane and nowhere
// left of it — but it is spent on the generous timeout for workerd cold-start ALONE. This
// lane runs NO retry, deliberately; the note on `fileParallelism` below says why.
export default defineConfig({
  plugins: [
    cloudflareTest({
      // Relative to this file's dir. The test-scoped wrangler config binds the
      // placeholder Coordinator Durable Object; its `main` worker supplies the entry.
      wrangler: { configPath: "./wrangler.jsonc" },
    }),
  ],
  test: {
    // the identity SHARED GLOBAL is now GONE: `regId` derives from the DO name
    // (identity = address), so `scope()` no longer writes `env.REGISTRY_ID` and the routine per-file
    // identity race the (i) containment guarded is killed (only the deliberate divergence/fallback
    // scenes touch `REGISTRY_ID`, each set-and-cleaned-up under this serialization). Serialization
    // is KEPT for now purely for the OTHER reason (i) cited: the pool-workers isolated-storage stack
    // bug under concurrent DO requests (workers-sdk #11031, `@cloudflare/vitest-pool-workers` pinned
    //). Re-evaluate lifting this once #11031 is resolved (the identity race no longer
    // requires it).
    //
    // NO `retry` HERE, AND THE ABSENCE IS LOAD-BEARING. A retry cannot remedy this
    // lane's known timing failure: the state it would have to reset is #11031's isolated Durable
    // Object storage, which is the subject of the failure itself. It also degrades the signal,
    // because a second attempt against storage that never reset reports values that read as a
    // different defect than the one that occurred. First failures are therefore the honest signal;
    // the remedy is a fresh-runner re-run at the CI level, logged rather than
    // silent. Do not restore a retry here — a genuinely distinct transient class earns its own
    // adjudication instead.
    //
    // WHAT A GREEN IN THIS LANE CERTIFIES: driver behaviour under the runner's timing regime,
    // which is an environmental parameter and not a property of production. Expect reds on the current
    // fleet, and TRIAGE THEM BEFORE FILING THEM. A red matching the known alarm race — a
    // `runDurableObjectAlarm` assertion reading false, in a suite that still asks for the alarm
    // without awaiting it into existence — is diagnosed against that signature and logged against
    // that known race. **Any other red is a real finding and is treated
    // as one.** The distinction matters more than it looks: this file's guidance must never become
    // a blanket instruction to attribute every failure in the lane to a known cause, because that
    // is how a genuine regression hides inside a familiar ticket. The known race also motivates converting
    // the conditional into a deterministic harness — one that awaits the conditions it observes
    // into existence rather than racing them.
    fileParallelism: false,
    hookTimeout: 30_000,
    include: ["**/*.test.ts"],
    name: "integration",
    setupFiles: [
      fileURLToPath(new URL("./alarm-failure-diagnostics.ts", import.meta.url)),
      fileURLToPath(new URL("./manual-alarm-clock.ts", import.meta.url)),
    ],
    testTimeout: 30_000,
  },
});
