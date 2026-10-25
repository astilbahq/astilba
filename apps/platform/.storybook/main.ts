import type { StorybookConfig } from "@storybook/tanstack-react";

const config: StorybookConfig = {
  framework: {
    name: "@storybook/tanstack-react",
    options: { builder: { viteConfigPath: ".storybook/vite.config.ts" } },
  },
  stories: ["../src/**/*.stories.tsx"],
  addons: ["@storybook/addon-a11y", "msw-storybook-addon"],
  staticDirs: ["./public"],
  core: { disableTelemetry: true },
  typescript: { reactDocgen: false },
  async viteFinal(config) {
    // Workshop requests have no path to the application's backend proxy.
    config.server = { ...config.server, proxy: {}, host: "127.0.0.1" };
    return config;
  },
};

export default config;
