import { defineConfig, devices } from "@playwright/test";
import { portBlock, reservePortBlock } from "../../scripts/test-ports.mjs";

// The main process reserves this run's block and the workers inherit it. The preview
// takes the block's second port.
process.env.PLAT_BROWSER_PORT_BASE ??= String(await reservePortBlock());
const workshop = `http://127.0.0.1:${portBlock() + 1}`;

export default defineConfig({
  testDir: "workshop-tests",
  fullyParallel: true,
  workers: 2,
  timeout: 30000,
  outputDir: "../../.artifacts/storybook/test-results",
  use: {
    baseURL: workshop,
    trace: "retain-on-failure",
    ...devices["Desktop Chrome"],
  },
  projects: [
    { name: "chromium" },
    { name: "webkit", use: { ...devices["Desktop Safari"] } },
  ],
  webServer: {
    command:
      "vp preview --config .storybook/vite.config.ts --outDir ../../.artifacts/storybook/static --host 127.0.0.1 --strictPort --port " +
      String(portBlock() + 1),
    url: workshop,
    reuseExistingServer: false,
  },
});
