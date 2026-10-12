import fc from "fast-check";

// Vitest setup file (wired via vite.config `setupFiles`). Pins fast-check's global
// config so property runs are reproducible: a fixed base seed + `numRuns`. On
// failure fast-check prints the `{seed, path}` needed to replay the exact run.

/** The fixed fast-check base seed for this repo's property suite. */
export const FC_BASE_SEED = 0x5e_ed;

fc.configureGlobal({
  numRuns: 100,
  seed: FC_BASE_SEED,
});
