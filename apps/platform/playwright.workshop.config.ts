import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "workshop-tests",
  fullyParallel: true,
  workers: 2,
  timeout: 30000,
  outputDir: "../../.artifacts/storybook/test-results",
  use: {
    baseURL: "http://127.0.0.1:15201",
    trace: "retain-on-failure",
    ...devices["Desktop Chrome"],
  },
  projects: [
    { name: "chromium" },
    { name: "webkit", use: { ...devices["Desktop Safari"] } },
  ],
  webServer: {
    command:
      "vp preview --config .storybook/vite.config.ts --outDir ../../.artifacts/storybook/static --host 127.0.0.1 --port 15201 --strictPort",
    url: "http://127.0.0.1:15201",
    reuseExistingServer: false,
  },
});
