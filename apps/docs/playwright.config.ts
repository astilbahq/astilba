import { defineConfig, devices } from "@playwright/test";

const port = 4322;
const baseURL = `http://127.0.0.1:${port}`;
const wranglerLogFile = ".wrangler/playwright-dev.log";

export default defineConfig({
  testDir: "./tests/e2e",
  outputDir: "test-results",
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 2 : undefined,
  reporter: process.env.CI ? [["line"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL,
    contextOptions: {
      reducedMotion: "reduce",
    },
    screenshot: "only-on-failure",
    trace: "on-first-retry",
    video: "retain-on-failure",
  },
  webServer: {
    // Wrangler exits on EPIPE if Playwright closes its output pipes (workers-sdk#15202).
    command: `mkdir -p .wrangler && exec pnpm exec wrangler dev --local --ip 127.0.0.1 --port ${port} >"${wranglerLogFile}" 2>&1`,
    env: { FORCE_COLOR: "0" },
    reuseExistingServer: false,
    timeout: 120_000,
    url: `${baseURL}/docs/`,
  },
  projects: [
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        permissions: ["clipboard-read", "clipboard-write"],
      },
    },
  ],
});
