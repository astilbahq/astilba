import { defineConfig, devices } from "@playwright/test";
import {
  dashboardPorts,
  portBlock,
  reservePortBlock,
} from "../../scripts/test-ports.mjs";

// The main process reserves this run's ports; the fixture, Vite and the test workers
// it starts inherit the variable and read the same block.
process.env.PLAT_BROWSER_PORT_BASE ??= String(await reservePortBlock());
const ports = dashboardPorts();

export default defineConfig({
  testDir: "tests",
  fullyParallel: false,
  workers: 1,
  timeout: 60000,
  use: {
    ...devices["Desktop Chrome"],
    baseURL: `http://127.0.0.1:${ports.api}`,
    trace: "retain-on-failure",
  },
  // Playwright empties its output directory as a run starts, so each block has its own.
  outputDir: `../../.artifacts/p1/playwright/${portBlock()}`,
  reporter: "list",
  webServer: [
    {
      command: "node ../../scripts/platform-fixture.mjs",
      url: `http://127.0.0.1:${ports.api}/api/v1/session`,
      reuseExistingServer: false,
      gracefulShutdown: { signal: "SIGTERM", timeout: 15000 },
      timeout: 180000,
    },
    {
      command: "pnpm dev",
      env: {
        ASTILBA_DEV_UI_PORT: String(ports.dev),
        ASTILBA_DEV_API_PORT: String(ports.devApi),
      },
      url: `http://127.0.0.1:${ports.dev}`,
      reuseExistingServer: false,
      gracefulShutdown: { signal: "SIGTERM", timeout: 15000 },
    },
  ],
  projects: [
    { name: "chromium" },
    { name: "webkit", use: { ...devices["Desktop Safari"] } },
  ],
});
