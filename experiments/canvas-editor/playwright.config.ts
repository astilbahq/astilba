import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./tests",
  testMatch: "**/*.spec.ts",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 60_000,
  outputDir: "../../.artifacts/plat1/browser-results",
  reporter: [
    ["list"],
    ["json", { outputFile: "../../.artifacts/plat1/browser-report.json" }],
  ],
  use: {
    baseURL: "http://127.0.0.1:54171",
    viewport: { width: 1440, height: 1000 },
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: {
        ...devices["Desktop Chrome"],
        viewport: { width: 1440, height: 1000 },
      },
    },
    {
      name: "webkit",
      use: {
        ...devices["Desktop Safari"],
        viewport: { width: 1440, height: 1000 },
      },
    },
  ],
  webServer: {
    command: "pnpm preview",
    url: "http://127.0.0.1:54171",
    reuseExistingServer: false,
    timeout: 30_000,
  },
});
