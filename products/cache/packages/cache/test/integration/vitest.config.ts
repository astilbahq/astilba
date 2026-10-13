import { cloudflareTest } from "@cloudflare/vitest-pool-workers";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

// The C1-b integration lane (rule 12): the fourth test project, run on real workerd
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
    /* References: E19-b. */
    
    
    
    
    
    
    
    
    
    
    
    
    
    
    
    
    
    
    
    
    
    
    
    
    
    
    
    
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
