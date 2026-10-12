import { defineConfig, devices } from "@playwright/test";

const port = 4326;
const baseURL = `http://127.0.0.1:${port}`;

export default defineConfig({
  testDir: "./test/browser",
  fullyParallel: true,
  use: {
    baseURL,
    colorScheme: "dark",
    trace: "retain-on-failure",
  },
  webServer: {
    command: `pnpm build:production && pnpm preview --host 127.0.0.1 --port ${port}`,
    env: { ASTRO_PREVIEW_BACKGROUND: "0" },
    reuseExistingServer: false,
    timeout: 120_000,
    url: baseURL,
  },
  projects: [
    {
      name: "desktop-chromium",
      use: { ...devices["Desktop Chrome"] },
    },
    {
      name: "mobile-chromium",
      use: {
        browserName: "chromium",
        viewport: { height: 844, width: 390 },
      },
    },
  ],
});
