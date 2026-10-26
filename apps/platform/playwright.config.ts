import { defineConfig, devices } from "@playwright/test";
export default defineConfig({
  testDir: "tests",
  fullyParallel: false,
  workers: 1,
  timeout: 60000,
  use: {
    ...devices["Desktop Chrome"],
    baseURL: "http://127.0.0.1:15181",
    trace: "retain-on-failure",
  },
  outputDir: "../../.artifacts/p1/playwright",
  reporter: "list",
  webServer: [
    {
      command: "node ../../scripts/platform-fixture.mjs",
      url: "http://127.0.0.1:15183/api/v1/session",
      reuseExistingServer: false,
      gracefulShutdown: { signal: "SIGTERM", timeout: 15000 },
      timeout: 180000,
    },
    {
      command: "pnpm dev",
      env: { ASTILBA_DEV_UI_PORT: "15182", ASTILBA_DEV_API_PORT: "15189" },
      url: "http://127.0.0.1:15182",
      reuseExistingServer: false,
      gracefulShutdown: { signal: "SIGTERM", timeout: 15000 },
    },
  ],
  projects: [
    { name: "chromium" },
    { name: "webkit", use: { ...devices["Desktop Safari"] } },
  ],
});
